package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"filespace/backend/internal/domain"
)

// Errors returned by FileService's chunked-upload session methods. Handlers
// map these to HTTP status codes (see handler/files.go's upload-session
// routes).
var (
	ErrUploadSessionNotFound = errors.New("service: upload session not found")
	ErrUploadChunkOutOfOrder = errors.New("service: chunk index out of order")
	ErrUploadSizeMismatch    = errors.New("service: uploaded bytes do not match declared size")
	ErrUploadTooLarge        = errors.New("service: declared upload size exceeds the limit")
)

// UploadChunkSize is the chunk size InitiateUpload tells clients to use.
// Keeping requests this small matters in production: file-space is deployed
// behind a Cloudflare Tunnel (REQUIREMENTS.md §5.6), whose free-tier edge
// rejects request bodies past a size Cloudflare controls (historically
// 100MB) — a single 500MB POST would simply fail there regardless of what
// this server allows. Splitting into <=8MB chunks keeps every individual
// request comfortably under that, for any file size, and is what makes
// per-chunk upload progress meaningful client-side.
const UploadChunkSize = 8 << 20 // 8 MiB

// maxUploadSessionSize mirrors the old single-shot endpoint's cap: chunking
// solves the *per-request* size problem, not "how big a file are we willing
// to store," which stays a deliberate app-level limit.
const maxUploadSessionSize = 512 << 20 // 512 MiB

// uploadSessionTTL bounds how long an initiated-but-abandoned upload (no
// chunk or complete/abort call) keeps its scratch file and bookkeeping
// around before it's discarded automatically.
const uploadSessionTTL = 30 * time.Minute

// uploadSession is one in-progress chunked upload: a scratch file being
// appended to strictly in order, plus enough bookkeeping to validate chunk
// order and finalize into a real stored file once every chunk has arrived.
type uploadSession struct {
	mu         sync.Mutex
	uploadedBy int64
	filename   string
	declared   int64
	received   int64
	nextChunk  int
	file       *os.File
	expireAt   *time.Timer
}

// uploadSessionManager tracks in-progress chunked uploads in memory, keyed
// by a random id. Sessions are server-instance-local and are never
// persisted — an interrupted upload cannot be resumed after a server
// restart, only retried from scratch. That's an acceptable trade-off for a
// single-instance, self-hosted personal drive (REQUIREMENTS.md's stated
// scope) rather than a full resumable-upload protocol.
//
// Scratch files live under the OS temp dir (os.CreateTemp("", ...)) rather
// than going through the FileStorage interface: they're transient staging
// buffers for a write still in progress, not "stored files" in the product
// sense, so the interface CLAUDE.md asks handlers/services to route storage
// through doesn't apply here — only the finished upload, handed to
// FileService.Create, is durably persisted via FileStorage.Save.
type uploadSessionManager struct {
	mu       sync.Mutex
	sessions map[string]*uploadSession
}

func newUploadSessionManager() *uploadSessionManager {
	return &uploadSessionManager{sessions: make(map[string]*uploadSession)}
}

func randomUploadID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("service: generate upload id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (m *uploadSessionManager) create(uploadedBy int64, filename string, size int64) (string, error) {
	f, err := os.CreateTemp("", "filespace-upload-*")
	if err != nil {
		return "", fmt.Errorf("service: create upload scratch file: %w", err)
	}
	id, err := randomUploadID()
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	sess := &uploadSession{uploadedBy: uploadedBy, filename: filename, declared: size, file: f}

	m.mu.Lock()
	sess.expireAt = time.AfterFunc(uploadSessionTTL, func() { m.expire(id) })
	m.sessions[id] = sess
	m.mu.Unlock()
	return id, nil
}

// get looks up id, scoped to uploadedBy so one user can never touch another
// user's in-progress upload even by guessing/reusing an id.
func (m *uploadSessionManager) get(uploadedBy int64, id string) (*uploadSession, error) {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok || sess.uploadedBy != uploadedBy {
		return nil, ErrUploadSessionNotFound
	}
	return sess, nil
}

// remove discards a session's bookkeeping and its scratch file. Safe to
// call more than once (a second call is a no-op).
func (m *uploadSessionManager) remove(id string) {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if !ok {
		return
	}
	sess.expireAt.Stop()
	sess.mu.Lock()
	path := sess.file.Name()
	_ = sess.file.Close()
	sess.mu.Unlock()
	_ = os.Remove(path)
}

func (m *uploadSessionManager) expire(id string) {
	log.Printf("service: upload session %q idle past %s, discarding", id, uploadSessionTTL)
	m.remove(id)
}

// InitiateUpload starts a new chunked-upload session for a file of the
// given declared size, returning the session id and the chunk size the
// caller must upload in (see UploadChunkSize).
func (s *FileService) InitiateUpload(ctx context.Context, uploadedBy int64, filename string, size int64) (uploadID string, chunkSize int64, err error) {
	if filename == "" {
		return "", 0, ErrInvalidFilename
	}
	if size < 0 || size > maxUploadSessionSize {
		return "", 0, ErrUploadTooLarge
	}
	id, err := s.uploads.create(uploadedBy, filename, size)
	if err != nil {
		return "", 0, err
	}
	return id, UploadChunkSize, nil
}

// UploadChunk appends the bytes read from r to uploadID's scratch file.
// Chunks must arrive strictly in order starting at index 0 — the server
// only ever appends, so an out-of-order or repeated index is rejected
// rather than silently accepted in the wrong position.
func (s *FileService) UploadChunk(ctx context.Context, uploadedBy int64, uploadID string, index int, r io.Reader) error {
	sess, err := s.uploads.get(uploadedBy, uploadID)
	if err != nil {
		return err
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	if index != sess.nextChunk {
		return ErrUploadChunkOutOfOrder
	}
	n, err := io.Copy(sess.file, r)
	if err != nil {
		return fmt.Errorf("service: write upload chunk: %w", err)
	}
	sess.received += n
	sess.nextChunk++
	sess.expireAt.Reset(uploadSessionTTL)
	return nil
}

// CompleteUpload finalizes uploadID: verifies every declared byte arrived,
// then hands the reassembled content to Create exactly as the single-shot
// upload path does, and always discards the session (success or failure)
// so a session can never be completed twice.
func (s *FileService) CompleteUpload(ctx context.Context, uploadedBy int64, uploadID string) (domain.File, error) {
	sess, err := s.uploads.get(uploadedBy, uploadID)
	if err != nil {
		return domain.File{}, err
	}
	defer s.uploads.remove(uploadID)

	sess.mu.Lock()
	defer sess.mu.Unlock()

	if sess.received != sess.declared {
		return domain.File{}, ErrUploadSizeMismatch
	}
	if _, err := sess.file.Seek(0, io.SeekStart); err != nil {
		return domain.File{}, fmt.Errorf("service: rewind upload scratch file: %w", err)
	}
	return s.Create(ctx, uploadedBy, sess.filename, sess.file)
}

// AbortUpload discards an in-progress upload (e.g. the user cancels, or the
// client gives up after a failed chunk) and cleans up its scratch file.
func (s *FileService) AbortUpload(ctx context.Context, uploadedBy int64, uploadID string) error {
	if _, err := s.uploads.get(uploadedBy, uploadID); err != nil {
		return err
	}
	s.uploads.remove(uploadID)
	return nil
}
