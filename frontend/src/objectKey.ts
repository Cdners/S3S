// Textareas display CR and CRLF as LF. Map edits back to the original key so
// appending a name to an existing prefix cannot silently rename that prefix.
export function displayKey(key: string): string {
  return key.replace(/\r\n?/g, '\n')
}

function sourceOffset(key: string, displayOffset: number): number {
  let source = 0
  for (let i = 0; i < displayOffset; i++) {
    if (key[source] === '\r' && key[source + 1] === '\n') source++
    source++
  }
  return source
}

export function replaceKeySelection(key: string, start: number, end: number, text: string): string {
  return key.slice(0, sourceOffset(key,start)) + text + key.slice(sourceOffset(key,end))
}

export function editKey(key: string, value: string): string {
  if (!key.includes('\r')) return value
  const old = displayKey(key)
  let start = 0
  while (start < old.length && start < value.length && old[start] === value[start]) start++
  let oldEnd = old.length
  let end = value.length
  while (oldEnd > start && end > start && old[oldEnd-1] === value[end-1]) {
    oldEnd--
    end--
  }
  return replaceKeySelection(key, start, oldEnd, value.slice(start,end))
}

export function validKey(key: string): boolean {
  // AWS and R2 limit the entire key to 1024 UTF-8 bytes.
  return key.length > 0 && new TextEncoder().encode(key).length <= 1024
}
