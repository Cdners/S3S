import test from 'node:test'
import assert from 'node:assert/strict'
import en from '../src/locales/en-US.json' with { type: 'json' }
import zh from '../src/locales/zh-CN.json' with { type: 'json' }
import { parameters, parsePack, translate } from '../src/catalog.ts'

test('built-ins contain the same keys and parameter multisets, including native surfaces', () => {
  assert.deepEqual(Object.keys(en).sort(), Object.keys(zh).sort())
  for (const key of Object.keys(en)) {
    assert.ok(en[key].trim() && zh[key].trim(), key)
    assert.deepEqual(parameters(en[key]), parameters(zh[key]), key)
  }
  for (const prefix of ['native.menu.', 'native.tray.', 'native.notification.']) {
    assert.ok(Object.keys(en).some(key => key.startsWith(prefix)), prefix)
  }
})

test('a partial imported pack canonicalizes its locale, interpolates supplied values and falls back', () => {
  const pack = parsePack({ locale:'fr-ca', name:'Français', messages:{'connections.count':'Profils : {count}'} }, en)
  assert.equal(pack.locale, 'fr-CA')
  assert.equal(translate('connections.count', pack.messages, en, {count:2}), 'Profils : 2')
  assert.equal(translate('nav.settings', pack.messages, en), en['nav.settings'])
  assert.equal(translate('connections.actionEdit', pack.messages, en, {name:'a$&{count}'}), 'Edit a$&{count}')
})

test('malformed packs, built-in replacement and placeholder corruption are rejected', () => {
  const pack = { locale:'fr', name:'Français', messages:{'nav.settings':'Réglages'} }
  for (const candidate of [null, [], {...pack, locale:'EN-us'}, {...pack, locale:'bad_locale'}, {...pack,name:''},
    {...pack,messages:{}}, {...pack,messages:{'unknown.key':'?'}}, {...pack,messages:{'nav.settings':5}},
    {...pack,messages:{'connections.count':'Profils'}}, {...pack,messages:{'nav.settings':'Réglages {new}'}}]) {
    assert.throws(() => parsePack(candidate, en))
  }
})
