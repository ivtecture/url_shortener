const crypto = require('node:crypto');

const ALPHABET = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789';
const CODE_PATTERN = /^[a-zA-Z0-9_-]{3,32}$/;

function generateCode(length = 6) {
  const bytes = crypto.randomBytes(length);
  let out = '';
  for (const b of bytes) out += ALPHABET[b % ALPHABET.length];
  return out;
}

function isValidCustomCode(code) {
  return CODE_PATTERN.test(code);
}

module.exports = { generateCode, isValidCustomCode };