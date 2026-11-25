// One instance per listing scope. Changing scope invalidates every earlier request,
// including in-flight pages that settle after the profile, bucket or prefix changes.
export interface Page<T> { entries: T[]; nextToken: string }

export class Listing<T> {
  entries: T[] = []
  nextToken = ''
  loading = false
  loaded = false
  private generation = 0
  private readonly consumed = new Set<string>()

  reset() {
    this.generation++
    this.entries = []
    this.nextToken = ''
    this.loading = false
    this.loaded = false
    this.consumed.clear()
  }

  async load(fetchPage: (token: string) => Promise<Page<T>>) {
    if (this.loading || (this.loaded && !this.nextToken)) return
    const generation = this.generation
    const token = this.nextToken
    this.loading = true
    try {
      const result = await fetchPage(token)
      if (generation !== this.generation) return
      if (result.nextToken && (result.nextToken === token || this.consumed.has(result.nextToken))) {
        throw new PaginationError()
      }
      this.consumed.add(token)
      this.entries = this.entries.concat(result.entries)
      this.nextToken = result.nextToken
      this.loaded = true
    } catch (error) {
      if (generation === this.generation) throw error
    } finally {
      if (generation === this.generation) this.loading = false
    }
  }
}

export class PaginationError extends Error {}

// Prefixes are remote key strings with "/" as the listing delimiter. Slice only
// the last component, retaining repeated slashes, dot segments and Unicode.
export function parentPrefix(prefix: string): string {
  const end = prefix.endsWith('/') ? prefix.length - 1 : prefix.length
  return prefix.slice(0, prefix.slice(0, end).lastIndexOf('/') + 1)
}
