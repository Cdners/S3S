import { test } from 'node:test'
import assert from 'node:assert/strict'
import { Listing, PaginationError, parentPrefix } from '../src/listing.ts'

test('switching accounts or prefixes discards stale success, failure and loading state', async () => {
  for (const staleFails of [false, true]) {
    const listing = new Listing()
    const oldPage = Promise.withResolvers()
    const currentPage = Promise.withResolvers()
    const first = listing.load(() => oldPage.promise)
    listing.reset()
    const second = listing.load(() => currentPage.promise)
    if (staleFails) oldPage.reject(new Error('old error'))
    else oldPage.resolve({entries:['old key'], nextToken:'old token'})
    await first
    assert.deepEqual(listing.entries, [])
    assert.equal(listing.loading, true)
    currentPage.resolve({entries:['/新//../桶'], nextToken:''})
    await second
    assert.deepEqual(listing.entries, ['/新//../桶'])
    assert.equal(listing.loading, false)
  }
})

test('page append uses opaque tokens, retains entries on error and rejects token loops', async () => {
  const list = new Listing()
  const cursor = '/二?.+=X'
  await list.load(async token => {
    assert.equal(token, '')
    return {entries:['a'], nextToken:cursor}
  })
  await assert.rejects(list.load(async () => { throw Error('connection lost') }))
  assert.deepEqual(list.entries, ['a'])
  assert.equal(list.nextToken, cursor)
  await assert.rejects(list.load(async () => ({entries:['x'],nextToken:cursor})), PaginationError)
  await list.load(async token => {
    assert.equal(token, cursor)
    return {entries:['b'],nextToken:'two'}
  })
  await assert.rejects(list.load(async () => ({entries:[], nextToken:cursor})), PaginationError)
  await list.load(async () => ({entries:['c'], nextToken:''}))
  await list.load(() => assert.fail('end of listing must not request first page again'))
  assert.deepEqual(list.entries, ['a','b','c'])
})

test('parent prefix preserves repeated delimiters and literal dot segments', () => {
  for (const [prefix, parent] of [['',''],['/',''],['a//','a/'],['/a/','/'],['x/.././','x/../'],['前缀/空 格/','前缀/']]) {
    assert.equal(parentPrefix(prefix),parent)
  }
})
