/** Hex-encoded SHA-256, matching desktop's FolderScanner.sha256Hex — used to
 * build each manifest entry's `sha256` field (REQUIREMENTS.md §6.1). */
export async function sha256Hex(data: ArrayBuffer): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', data)
  return Array.from(new Uint8Array(digest))
    .map((byte) => byte.toString(16).padStart(2, '0'))
    .join('')
}
