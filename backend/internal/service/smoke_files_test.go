//go:build smoke

package service

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"filespace/backend/internal/db"
	"filespace/backend/internal/repo"
	"filespace/backend/internal/storage"
)

// TestSmokeFileServiceRoundTrip exercises FileService against a real,
// live Postgres instance (SMOKE_DATABASE_URL) and a real LocalFileStorage
// rooted in a temp dir: uploads a .cs and a .jpg, reads them back via
// Get/Download/content-list/extension-filter/Delete, and checks the
// preview-vs-download extension gate.
func TestSmokeFileServiceRoundTrip(t *testing.T) {
	dsn := os.Getenv("SMOKE_DATABASE_URL")
	if dsn == "" {
		t.Skip("SMOKE_DATABASE_URL not set")
	}

	sqlxDB, err := db.Connect(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer sqlxDB.Close()

	store, err := storage.NewLocalFileStorage(filepath.Join(t.TempDir(), "storage"))
	if err != nil {
		t.Fatalf("new local storage: %v", err)
	}

	files := repo.NewPostgresFileRepo(sqlxDB)
	users := repo.NewPostgresUserRepo(sqlxDB)
	svc := NewFileService(files, store)

	// Users table has no pre-check for existing usernames at this layer;
	// create a couple of fresh ones scoped to this test run.
	ownerA, err := users.Create(t.Context(), "smoke_files_owner_a", "hash")
	if err != nil {
		t.Fatalf("create owner A: %v", err)
	}
	ownerB, err := users.Create(t.Context(), "smoke_files_owner_b", "hash")
	if err != nil {
		t.Fatalf("create owner B: %v", err)
	}

	// --- upload a .cs file ---
	csContent := []byte("public class Program { }\n")
	csFile, err := svc.Create(t.Context(), ownerA.ID, "Program.cs", bytes.NewReader(csContent))
	if err != nil {
		t.Fatalf("create .cs: %v", err)
	}
	if csFile.Extension != "cs" || csFile.Size != int64(len(csContent)) || csFile.UploadedBy != ownerA.ID || csFile.EditedBy != ownerA.ID {
		t.Fatalf("unexpected .cs metadata: %+v", csFile)
	}

	// --- upload a .jpg file ---
	jpgContent := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F'}
	jpgFile, err := svc.Create(t.Context(), ownerA.ID, "image.jpg", bytes.NewReader(jpgContent))
	if err != nil {
		t.Fatalf("create .jpg: %v", err)
	}
	if jpgFile.Extension != "jpg" || jpgFile.Size != int64(len(jpgContent)) {
		t.Fatalf("unexpected .jpg metadata: %+v", jpgFile)
	}

	// --- Get by id, scoped correctly ---
	got, err := svc.Get(t.Context(), ownerA.ID, csFile.ID)
	if err != nil || got.Name != "Program.cs" {
		t.Fatalf("get .cs: %+v, err=%v", got, err)
	}
	if _, err := svc.Get(t.Context(), ownerB.ID, csFile.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("get .cs as wrong owner: err = %v, want ErrNotFound", err)
	}

	// --- List with extension filter ---
	csOnly, err := svc.List(t.Context(), repo.ListParams{UploadedBy: ownerA.ID, Extension: "cs"})
	if err != nil {
		t.Fatalf("list extension=cs: %v", err)
	}
	if len(csOnly) != 1 || csOnly[0].ID != csFile.ID {
		t.Fatalf("list extension=cs: got %+v", csOnly)
	}

	all, err := svc.List(t.Context(), repo.ListParams{UploadedBy: ownerA.ID})
	if err != nil || len(all) != 2 {
		t.Fatalf("list all: got %+v, err=%v", all, err)
	}

	// --- Content: .cs is not previewable (only java/png are) -> 415-shaped error ---
	if _, _, err := svc.Content(t.Context(), ownerA.ID, csFile.ID); !errors.Is(err, ErrPreviewNotSupported) {
		t.Fatalf("content .cs: err = %v, want ErrPreviewNotSupported", err)
	}

	// --- upload a .java file to exercise the previewable path end-to-end ---
	javaContent := []byte("public class Main {}\n")
	javaFile, err := svc.Create(t.Context(), ownerA.ID, "Main.java", bytes.NewReader(javaContent))
	if err != nil {
		t.Fatalf("create .java: %v", err)
	}
	meta, rc, err := svc.Content(t.Context(), ownerA.ID, javaFile.ID)
	if err != nil {
		t.Fatalf("content .java: %v", err)
	}
	gotJava, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(gotJava, javaContent) {
		t.Fatalf("content .java: bytes mismatch, err=%v", err)
	}
	if ContentType(meta.Extension) != "text/plain; charset=utf-8" {
		t.Fatalf("ContentType(java) = %q", ContentType(meta.Extension))
	}

	// --- Download works for the non-previewable .cs and .jpg too ---
	_, rc, err = svc.Download(t.Context(), ownerA.ID, csFile.ID)
	if err != nil {
		t.Fatalf("download .cs: %v", err)
	}
	gotCS, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(gotCS, csContent) {
		t.Fatalf("download .cs: bytes mismatch, err=%v", err)
	}

	_, rc, err = svc.Download(t.Context(), ownerA.ID, jpgFile.ID)
	if err != nil {
		t.Fatalf("download .jpg: %v", err)
	}
	gotJPG, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(gotJPG, jpgContent) {
		t.Fatalf("download .jpg: bytes mismatch, err=%v", err)
	}

	if _, _, err := svc.Download(t.Context(), ownerB.ID, jpgFile.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("download .jpg as wrong owner: err = %v, want ErrNotFound", err)
	}

	// --- Delete refuses the wrong owner, then succeeds for the real one,
	// and the storage object is actually gone. ---
	if err := svc.Delete(t.Context(), ownerB.ID, csFile.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("delete .cs as wrong owner: err = %v, want ErrNotFound", err)
	}
	if err := svc.Delete(t.Context(), ownerA.ID, csFile.ID); err != nil {
		t.Fatalf("delete .cs: %v", err)
	}
	if _, err := svc.Get(t.Context(), ownerA.ID, csFile.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("get .cs after delete: err = %v, want ErrNotFound", err)
	}
	if _, err := store.Open(t.Context(), csFile.StorageKey); err == nil {
		t.Fatalf("storage key %q for .cs still readable after delete", csFile.StorageKey)
	}
}
