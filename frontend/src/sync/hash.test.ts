import { describe, expect, it } from 'vitest'
import { sha256Hex } from './hash'

describe('sync/hash', () => {
  it('matches the known sha256("") test vector', async () => {
    const digest = await sha256Hex(new ArrayBuffer(0))
    expect(digest).toBe('e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855')
  })

  it('hashes non-empty content', async () => {
    const digest = await sha256Hex(new TextEncoder().encode('hello world').buffer as ArrayBuffer)
    expect(digest).toBe('b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9')
  })
})
