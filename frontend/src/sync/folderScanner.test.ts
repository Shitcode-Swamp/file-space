import { describe, expect, it } from 'vitest'
import { scan } from './folderScanner'

function fakeFileHandle(name: string, content: string, lastModified = 1700000000000) {
  return {
    kind: 'file' as const,
    name,
    getFile: async () => new File([content], name, { lastModified }),
  }
}

function fakeDirHandle(
  entries: Array<{ kind: 'file' | 'directory'; name: string; getFile?: () => Promise<File> }>,
): FileSystemDirectoryHandle {
  return {
    kind: 'directory',
    name: 'root',
    async *values() {
      for (const entry of entries) yield entry
    },
  } as unknown as FileSystemDirectoryHandle
}

describe('sync/folderScanner', () => {
  it('scans regular files and computes size/mtime/sha256', async () => {
    const dir = fakeDirHandle([fakeFileHandle('a.txt', 'hello')])
    const result = await scan(dir)

    expect(result.entries.size).toBe(1)
    const entry = result.entries.get('a.txt')
    expect(entry?.size).toBe(5)
    expect(entry?.mtime).toBe(1700000000000)
    expect(entry?.sha256).toBe('2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824')
    expect(result.unreadableNames.size).toBe(0)
  })

  it('skips dotfiles and subdirectories', async () => {
    const dir = fakeDirHandle([
      fakeFileHandle('visible.txt', 'hi'),
      fakeFileHandle('.hidden.txt', 'secret'),
      { kind: 'directory', name: 'subdir' },
    ])
    const result = await scan(dir)

    expect(Array.from(result.entries.keys())).toEqual(['visible.txt'])
  })

  it('reports a file that throws on getFile() as unreadable, not missing', async () => {
    const broken = {
      kind: 'file' as const,
      name: 'locked.txt',
      getFile: async () => {
        throw new Error('permission denied')
      },
    }
    const dir = fakeDirHandle([fakeFileHandle('visible.txt', 'hi'), broken])
    const result = await scan(dir)

    expect(Array.from(result.entries.keys())).toEqual(['visible.txt'])
    expect(result.unreadableNames.has('locked.txt')).toBe(true)
  })
})
