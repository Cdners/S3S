import { test } from 'node:test'
import assert from 'node:assert/strict'
import { displayKey, editKey, replaceKeySelection, validKey } from '../src/objectKey.ts'
import { taskActions } from '../src/transferActions.ts'

test('uploads expose cleanup separately from resuming transfers', () => {
  const upload = {direction:'upload', resumable:true}
  for (const state of ['queued','paused','failed']) {
    assert.deepEqual(taskActions({...upload,state}), ['resume','cancel'])
    assert.deepEqual(taskActions({...upload,state,cancelRequested:true}), ['cleanup'])
  }
  assert.deepEqual(taskActions({...upload,state:'running'}), ['pause','cancel'])
  assert.deepEqual(taskActions({...upload,state:'running',cancelRequested:true}), ['cleanup'])
  for (const state of ['completed','canceled','unsupported']) {
    assert.deepEqual(taskActions({...upload,state}), [])
    assert.deepEqual(taskActions({...upload,state,cancelRequested:true}), [])
  }
  assert.deepEqual(taskActions({...upload,state:'running',resumable:false}), [])
})

test('download-only recovery controls are preserved', () => {
  const task = {direction:'download',resumable:true}
  assert.deepEqual(taskActions({...task,state:'running'}), ['pause'])
  assert.deepEqual(taskActions({...task,state:'paused'}), ['resume'])
  assert.deepEqual(taskActions({...task,state:'failed',resumable:false}), [])
})

test('upload key accepts raw spaces, dot segments and delimiters and enforces the UTF-8 byte boundary', () => {
  for (const key of ['  ', '/前缀//.././file ?%# ', 'folder/', '😀'.repeat(256)]) assert.ok(validKey(key))
  assert.equal(validKey(''), false)
  assert.equal(validKey('😀'.repeat(256) + 'a'), false)
})


test('editing a key preserves line endings inside a remote prefix and in pasted text', () => {
  const original = '🍃/a\r\nb\rc/'
  const display = displayKey(original)
  assert.equal(editKey(original, display + 'file'), original + 'file')
  assert.equal(editKey(original, display.replace('b','🐝')), original.replace('b','🐝'))
  assert.equal(editKey(original, 'unrelated'), 'unrelated')
  assert.equal(replaceKeySelection(original, 0, display.length, 'foo\r\nbar'), 'foo\r\nbar')
})
