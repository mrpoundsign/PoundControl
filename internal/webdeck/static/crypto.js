/* PoundDeck Pure-JS Cryptographic Module (HTTP-compatible, zero external dependencies)
 * Implements SHA-256, SHA-512, HMAC-SHA256, HKDF-SHA256, Ed25519, X25519, RFC 9382 SPAKE2,
 * and PoundDeck WebSocket mutual-auth / per-frame MAC helpers.
 */
(function (root, factory) {
  'use strict';
  const api = factory();
  if (typeof module === 'object' && module.exports) {
    module.exports = api;
  } else {
    root.PoundDeckCrypto = api;
  }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  const nodeCrypto = (typeof require === 'function' && typeof window === 'undefined')
    ? require('node:crypto')
    : null;

  function randomBytes(len) {
    const out = new Uint8Array(len);
    if (typeof globalThis !== 'undefined' && globalThis.crypto && typeof globalThis.crypto.getRandomValues === 'function') {
      globalThis.crypto.getRandomValues(out);
      return out;
    }
    if (nodeCrypto) {
      return new Uint8Array(nodeCrypto.randomBytes(len));
    }
    throw new Error('No secure random number generator available');
  }

  function utf8Bytes(str) {
    return new TextEncoder().encode(str);
  }

  function concatBytes(...parts) {
    let total = 0;
    for (const p of parts) {
      total += p.length;
    }
    const out = new Uint8Array(total);
    let offset = 0;
    for (const p of parts) {
      out.set(p, offset);
      offset += p.length;
    }
    return out;
  }

  function constantTimeEqual(a, b) {
    if (!a || !b || a.length !== b.length) {
      return false;
    }
    let diff = 0;
    for (let i = 0; i < a.length; i++) {
      diff |= a[i] ^ b[i];
    }
    return diff === 0;
  }

  function toBase64(bytes) {
    if (typeof Buffer !== 'undefined') {
      return Buffer.from(bytes).toString('base64');
    }
    let bin = '';
    const chunk = 0x8000;
    for (let i = 0; i < bytes.length; i += chunk) {
      bin += String.fromCharCode(...bytes.subarray(i, i + chunk));
    }
    return btoa(bin);
  }

  function fromBase64(str) {
    const clean = String(str || '').trim().replace(/-/g, '+').replace(/_/g, '/');
    const pad = clean.length % 4 === 0 ? '' : '='.repeat(4 - (clean.length % 4));
    const padded = clean + pad;
    if (typeof Buffer !== 'undefined') {
      return new Uint8Array(Buffer.from(padded, 'base64'));
    }
    const bin = atob(padded);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) {
      out[i] = bin.charCodeAt(i);
    }
    return out;
  }

  function toBase64Url(bytes) {
    return toBase64(bytes).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  }

  function fromHex(hex) {
    const clean = hex.trim();
    const out = new Uint8Array(clean.length / 2);
    for (let i = 0; i < out.length; i++) {
      out[i] = parseInt(clean.slice(i * 2, i * 2 + 2), 16);
    }
    return out;
  }

  function toHex(bytes) {
    let out = '';
    for (let i = 0; i < bytes.length; i++) {
      out += bytes[i].toString(16).padStart(2, '0');
    }
    return out;
  }

  function u64le(n) {
    const out = new Uint8Array(8);
    let v = BigInt(n);
    for (let i = 0; i < 8; i++) {
      out[i] = Number(v & 0xffn);
      v >>= 8n;
    }
    return out;
  }

  // ---- SHA-256 (FIPS 180-4) ----
  const SHA256_K = new Uint32Array([
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
  ]);

  function rotr32(v, n) {
    return (v >>> n) | (v << (32 - n));
  }

  function sha256(data) {
    const input = data instanceof Uint8Array ? data : new Uint8Array(data);
    const bitLen = BigInt(input.length) * 8n;
    const rem = (input.length + 1 + 8) % 64;
    const padLen = rem === 0 ? 0 : 64 - rem;
    const padded = new Uint8Array(input.length + 1 + padLen + 8);
    padded.set(input, 0);
    padded[input.length] = 0x80;
    const dv = new DataView(padded.buffer);
    dv.setUint32(padded.length - 8, Number((bitLen >> 32n) & 0xffffffffn), false);
    dv.setUint32(padded.length - 4, Number(bitLen & 0xffffffffn), false);

    let h0 = 0x6a09e667;
    let h1 = 0xbb67ae85;
    let h2 = 0x3c6ef372;
    let h3 = 0xa54ff53a;
    let h4 = 0x510e527f;
    let h5 = 0x9b05688c;
    let h6 = 0x1f83d9ab;
    let h7 = 0x5be0cd19;

    const w = new Uint32Array(64);
    for (let off = 0; off < padded.length; off += 64) {
      for (let i = 0; i < 16; i++) {
        w[i] = dv.getUint32(off + i * 4, false);
      }
      for (let i = 16; i < 64; i++) {
        const s0 = rotr32(w[i - 15], 7) ^ rotr32(w[i - 15], 18) ^ (w[i - 15] >>> 3);
        const s1 = rotr32(w[i - 2], 17) ^ rotr32(w[i - 2], 19) ^ (w[i - 2] >>> 10);
        w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0;
      }

      let a = h0;
      let b = h1;
      let c = h2;
      let d = h3;
      let e = h4;
      let f = h5;
      let g = h6;
      let h = h7;

      for (let i = 0; i < 64; i++) {
        const S1 = rotr32(e, 6) ^ rotr32(e, 11) ^ rotr32(e, 25);
        const ch = (e & f) ^ (~e & g);
        const temp1 = (h + S1 + ch + SHA256_K[i] + w[i]) >>> 0;
        const S0 = rotr32(a, 2) ^ rotr32(a, 13) ^ rotr32(a, 22);
        const maj = (a & b) ^ (a & c) ^ (b & c);
        const temp2 = (S0 + maj) >>> 0;

        h = g;
        g = f;
        f = e;
        e = (d + temp1) >>> 0;
        d = c;
        c = b;
        b = a;
        a = (temp1 + temp2) >>> 0;
      }

      h0 = (h0 + a) >>> 0;
      h1 = (h1 + b) >>> 0;
      h2 = (h2 + c) >>> 0;
      h3 = (h3 + d) >>> 0;
      h4 = (h4 + e) >>> 0;
      h5 = (h5 + f) >>> 0;
      h6 = (h6 + g) >>> 0;
      h7 = (h7 + h) >>> 0;
    }

    const out = new Uint8Array(32);
    const outDv = new DataView(out.buffer);
    outDv.setUint32(0, h0, false);
    outDv.setUint32(4, h1, false);
    outDv.setUint32(8, h2, false);
    outDv.setUint32(12, h3, false);
    outDv.setUint32(16, h4, false);
    outDv.setUint32(20, h5, false);
    outDv.setUint32(24, h6, false);
    outDv.setUint32(28, h7, false);
    return out;
  }

  // ---- SHA-512 (FIPS 180-4) using BigInt 64-bit words ----
  const MASK64 = 0xffffffffffffffffn;
  const SHA512_K = [
    0x428a2f98d728ae22n, 0x7137449123ef65cdn, 0xb5c0fbcfec4d3b2fn, 0xe9b5dba58189dbbcn,
    0x3956c25bf348b538n, 0x59f111f1b605d019n, 0x923f82a4af194f9bn, 0xab1c5ed5da6d8118n,
    0xd807aa98a3030242n, 0x12835b0145706fben, 0x243185be4ee4b28cn, 0x550c7dc3d5ffb4e2n,
    0x72be5d74f27b896fn, 0x80deb1fe3b1696b1n, 0x9bdc06a725c71235n, 0xc19bf174cf692694n,
    0xe49b69c19ef14ad2n, 0xefbe4786384f25e3n, 0x0fc19dc68b8cd5b5n, 0x240ca1cc77ac9c65n,
    0x2de92c6f592b0275n, 0x4a7484aa6ea6e483n, 0x5cb0a9dcbd41fbd4n, 0x76f988da831153b5n,
    0x983e5152ee66dfabn, 0xa831c66d2db43210n, 0xb00327c898fb213fn, 0xbf597fc7beef0ee4n,
    0xc6e00bf33da88fc2n, 0xd5a79147930aa725n, 0x06ca6351e003826fn, 0x142929670a0e6e70n,
    0x27b70a8546d22ffcn, 0x2e1b21385c26c926n, 0x4d2c6dfc5ac42aedn, 0x53380d139d95b3dfn,
    0x650a73548baf63den, 0x766a0abb3c77b2a8n, 0x81c2c92e47edaee6n, 0x92722c851482353bn,
    0xa2bfe8a14cf10364n, 0xa81a664bbc423001n, 0xc24b8b70d0f89791n, 0xc76c51a30654be30n,
    0xd192e819d6ef5218n, 0xd69906245565a910n, 0xf40e35855771202an, 0x106aa07032bbd1b8n,
    0x19a4c116b8d2d0c8n, 0x1e376c085141ab53n, 0x2748774cdf8eeb99n, 0x34b0bcb5e19b48a8n,
    0x391c0cb3c5c95a63n, 0x4ed8aa4ae3418acbn, 0x5b9cca4f7763e373n, 0x682e6ff3d6b2b8a3n,
    0x748f82ee5defb2fcn, 0x78a5636f43172f60n, 0x84c87814a1f0ab72n, 0x8cc702081a6439ecn,
    0x90befffa23631e28n, 0xa4506cebde82bde9n, 0xbef9a3f7b2c67915n, 0xc67178f2e372532bn,
    0xca273eceea26619cn, 0xd186b8c721c0c207n, 0xeada7dd6cde0eb1en, 0xf57d4f7fee6ed178n,
    0x06f067aa72176fban, 0x0a637dc5a2c898a6n, 0x113f9804bef90daen, 0x1b710b35131c471bn,
    0x28db77f523047d84n, 0x32caab7b40c72493n, 0x3c9ebe0a15c9bebcn, 0x431d67c49c100d4cn,
    0x4cc5d4becb3e42b6n, 0x597f299cfc657e2an, 0x5fcb6fab3ad6faecn, 0x6c44198c4a475817n,
  ];

  function rotr64(v, n) {
    return ((v >> BigInt(n)) | (v << BigInt(64 - n))) & MASK64;
  }

  function sha512(data) {
    const input = data instanceof Uint8Array ? data : new Uint8Array(data);
    const bitLen = BigInt(input.length) * 8n;
    const rem = (input.length + 1 + 16) % 128;
    const padLen = rem === 0 ? 0 : 128 - rem;
    const padded = new Uint8Array(input.length + 1 + padLen + 16);
    padded.set(input, 0);
    padded[input.length] = 0x80;
    const dv = new DataView(padded.buffer);
    dv.setBigUint64(padded.length - 16, bitLen >> 64n, false);
    dv.setBigUint64(padded.length - 8, bitLen & MASK64, false);

    let h0 = 0x6a09e667f3bcc908n;
    let h1 = 0xbb67ae8584caa73bn;
    let h2 = 0x3c6ef372fe94f82bn;
    let h3 = 0xa54ff53a5f1d36f1n;
    let h4 = 0x510e527fade682d1n;
    let h5 = 0x9b05688c2b3e6c1fn;
    let h6 = 0x1f83d9abfb41bd6bn;
    let h7 = 0x5be0cd19137e2179n;

    const w = new Array(80);
    for (let off = 0; off < padded.length; off += 128) {
      for (let i = 0; i < 16; i++) {
        w[i] = dv.getBigUint64(off + i * 8, false);
      }
      for (let i = 16; i < 80; i++) {
        const s0 = rotr64(w[i - 15], 1) ^ rotr64(w[i - 15], 8) ^ (w[i - 15] >> 7n);
        const s1 = rotr64(w[i - 2], 19) ^ rotr64(w[i - 2], 61) ^ (w[i - 2] >> 6n);
        w[i] = (w[i - 16] + s0 + w[i - 7] + s1) & MASK64;
      }

      let a = h0;
      let b = h1;
      let c = h2;
      let d = h3;
      let e = h4;
      let f = h5;
      let g = h6;
      let h = h7;

      for (let i = 0; i < 80; i++) {
        const S1 = rotr64(e, 14) ^ rotr64(e, 18) ^ rotr64(e, 41);
        const ch = (e & f) ^ ((~e & MASK64) & g);
        const temp1 = (h + S1 + ch + SHA512_K[i] + w[i]) & MASK64;
        const S0 = rotr64(a, 28) ^ rotr64(a, 34) ^ rotr64(a, 39);
        const maj = (a & b) ^ (a & c) ^ (b & c);
        const temp2 = (S0 + maj) & MASK64;

        h = g;
        g = f;
        f = e;
        e = (d + temp1) & MASK64;
        d = c;
        c = b;
        b = a;
        a = (temp1 + temp2) & MASK64;
      }

      h0 = (h0 + a) & MASK64;
      h1 = (h1 + b) & MASK64;
      h2 = (h2 + c) & MASK64;
      h3 = (h3 + d) & MASK64;
      h4 = (h4 + e) & MASK64;
      h5 = (h5 + f) & MASK64;
      h6 = (h6 + g) & MASK64;
      h7 = (h7 + h) & MASK64;
    }

    const out = new Uint8Array(64);
    const outDv = new DataView(out.buffer);
    outDv.setBigUint64(0, h0, false);
    outDv.setBigUint64(8, h1, false);
    outDv.setBigUint64(16, h2, false);
    outDv.setBigUint64(24, h3, false);
    outDv.setBigUint64(32, h4, false);
    outDv.setBigUint64(40, h5, false);
    outDv.setBigUint64(48, h6, false);
    outDv.setBigUint64(56, h7, false);
    return out;
  }

  function hmacSha256(key, data) {
    let k = key instanceof Uint8Array ? key : new Uint8Array(key);
    if (k.length > 64) {
      k = sha256(k);
    }
    const keyBlock = new Uint8Array(64);
    keyBlock.set(k, 0);
    const oPad = new Uint8Array(64);
    const iPad = new Uint8Array(64);
    for (let i = 0; i < 64; i++) {
      oPad[i] = keyBlock[i] ^ 0x5c;
      iPad[i] = keyBlock[i] ^ 0x36;
    }
    const inner = sha256(concatBytes(iPad, data));
    return sha256(concatBytes(oPad, inner));
  }

  function hkdfSha256(ikm, salt, info, len) {
    const saltBytes = (salt && salt.length > 0) ? salt : new Uint8Array(32);
    const prk = hmacSha256(saltBytes, ikm);
    const infoBytes = typeof info === 'string' ? utf8Bytes(info) : (info || new Uint8Array(0));
    const blocks = Math.ceil(len / 32);
    const okm = new Uint8Array(blocks * 32);
    let prev = new Uint8Array(0);
    for (let i = 1; i <= blocks; i++) {
      prev = hmacSha256(prk, concatBytes(prev, infoBytes, new Uint8Array([i])));
      okm.set(prev, (i - 1) * 32);
    }
    return okm.slice(0, len);
  }

  // ---- Edwards25519 / Curve25519 Field & Group Arithmetic ----
  const P = (1n << 255n) - 19n;
  const L = (1n << 252n) + 27742317777372353535851937790883648493n;

  function mod(a, m) {
    const r = a % m;
    return r >= 0n ? r : r + m;
  }

  function modP(a) {
    const r = a % P;
    return r >= 0n ? r : r + P;
  }

  function powMod(base, exp, m) {
    let res = 1n;
    let b = mod(base, m);
    let e = exp;
    while (e > 0n) {
      if ((e & 1n) === 1n) {
        res = mod(res * b, m);
      }
      b = mod(b * b, m);
      e >>= 1n;
    }
    return res;
  }

  function invP(a) {
    return powMod(a, P - 2n, P);
  }

  const D = modP(-121665n * invP(121666n));
  const TWO_D = modP(2n * D);
  const SQRT_M1 = powMod(2n, (P - 1n) / 4n, P);

  function bytesToBigIntLE(bytes) {
    let r = 0n;
    for (let i = bytes.length - 1; i >= 0; i--) {
      r = (r << 8n) | BigInt(bytes[i]);
    }
    return r;
  }

  function bigIntToBytesLE(n, len) {
    const out = new Uint8Array(len);
    let v = n;
    for (let i = 0; i < len; i++) {
      out[i] = Number(v & 0xffn);
      v >>= 8n;
    }
    return out;
  }

  const POINT_ZERO = [0n, 1n, 1n, 0n];
  const GX = 15112221349535400772501151409588531511454012693041857206046113283949847762202n;
  const GY = 46316835694926478169428394003475163141307993866256225615783033603165251855960n;
  const POINT_G = [GX, GY, 1n, modP(GX * GY)];

  function pointAdd(p1, p2) {
    const [X1, Y1, Z1, T1] = p1;
    const [X2, Y2, Z2, T2] = p2;
    const A = modP((Y1 - X1) * (Y2 - X2));
    const B = modP((Y1 + X1) * (Y2 + X2));
    const C = modP(T1 * TWO_D * T2);
    const D_val = modP(Z1 * 2n * Z2);
    const E = modP(B - A);
    const F = modP(D_val - C);
    const G_val = modP(D_val + C);
    const H = modP(B + A);
    return [modP(E * F), modP(G_val * H), modP(F * G_val), modP(E * H)];
  }

  function pointNeg(p) {
    return [modP(-p[0]), p[1], p[2], modP(-p[3])];
  }

  function pointSub(p1, p2) {
    return pointAdd(p1, pointNeg(p2));
  }

  function pointMul(p, scalar) {
    let q = POINT_ZERO;
    let base = p;
    let k = scalar;
    while (k > 0n) {
      if ((k & 1n) === 1n) {
        q = pointAdd(q, base);
      }
      k >>= 1n;
      if (k > 0n) {
        base = pointAdd(base, base);
      }
    }
    return q;
  }

  function pointMultByCofactor(p) {
    let r = pointAdd(p, p);
    r = pointAdd(r, r);
    return pointAdd(r, r);
  }

  function pointEqual(p1, p2) {
    return modP(p1[0] * p2[2]) === modP(p2[0] * p1[2]) &&
      modP(p1[1] * p2[2]) === modP(p2[1] * p1[2]);
  }

  function pointIsIdentity(p) {
    return modP(p[0]) === 0n && modP(p[1]) === modP(p[2]);
  }

  function pointEncode(p) {
    const zInv = invP(p[2]);
    const x = modP(p[0] * zInv);
    const y = modP(p[1] * zInv);
    const out = bigIntToBytesLE(y, 32);
    if ((x & 1n) === 1n) {
      out[31] |= 0x80;
    }
    return out;
  }

  function pointDecode(bytes) {
    if (!(bytes instanceof Uint8Array) || bytes.length !== 32) {
      throw new Error('Invalid Edwards25519 point length');
    }
    const copy = new Uint8Array(bytes);
    const xSign = (copy[31] >> 7) & 1;
    copy[31] &= 0x7f;
    const y = bytesToBigIntLE(copy);
    if (y >= P) {
      throw new Error('Non-canonical Edwards25519 y-coordinate');
    }
    const y2 = modP(y * y);
    const u = modP(y2 - 1n);
    const v = modP(D * y2 + 1n);
    const v2 = modP(v * v);
    const v3 = modP(v2 * v);
    const v7 = modP(v3 * v3 * v);
    let x = modP(u * v3 * powMod(modP(u * v7), (P - 5n) / 8n, P));
    let vx2 = modP(v * x * x);
    if (vx2 !== u) {
      if (vx2 === modP(-u)) {
        x = modP(x * SQRT_M1);
        vx2 = modP(v * x * x);
      }
      if (vx2 !== u) {
        throw new Error('Point not on Edwards25519 curve');
      }
    }
    if (x === 0n && xSign === 1) {
      throw new Error('Invalid negative zero point coordinate');
    }
    if ((x & 1n) !== BigInt(xSign)) {
      x = modP(P - x);
    }
    return [x, y, 1n, modP(x * y)];
  }

  // ---- Ed25519 (RFC 8032) ----
  function clampEdSeedHash(h) {
    const aBytes = h.slice(0, 32);
    aBytes[0] &= 248;
    aBytes[31] &= 127;
    aBytes[31] |= 64;
    return bytesToBigIntLE(aBytes);
  }

  const ed25519 = {
    generateKeypair() {
      const seed = randomBytes(32);
      const publicKey = ed25519.publicKeyFromSeed(seed);
      return { seed, publicKey };
    },

    publicKeyFromSeed(seed) {
      if (!(seed instanceof Uint8Array) || seed.length !== 32) {
        throw new Error('Ed25519 seed must be 32 bytes');
      }
      const h = sha512(seed);
      const a = clampEdSeedHash(h);
      return pointEncode(pointMul(POINT_G, a));
    },

    sign(seedOrPriv, msg) {
      const seed = seedOrPriv.length === 64 ? seedOrPriv.slice(0, 32) : seedOrPriv;
      if (seed.length !== 32) {
        throw new Error('Ed25519 private key/seed invalid length');
      }
      const h = sha512(seed);
      const a = clampEdSeedHash(h);
      const prefix = h.slice(32, 64);
      const pub = pointEncode(pointMul(POINT_G, a));

      const r = mod(bytesToBigIntLE(sha512(concatBytes(prefix, msg))), L);
      const R = pointEncode(pointMul(POINT_G, r));
      const k = mod(bytesToBigIntLE(sha512(concatBytes(R, pub, msg))), L);
      const S = mod(r + k * a, L);
      return concatBytes(R, bigIntToBytesLE(S, 32));
    },

    verify(pub, msg, sig) {
      try {
        if (!(pub instanceof Uint8Array) || pub.length !== 32 || !(sig instanceof Uint8Array) || sig.length !== 64) {
          return false;
        }
        const A = pointDecode(pub);
        const RBytes = sig.slice(0, 32);
        const R = pointDecode(RBytes);
        const S = bytesToBigIntLE(sig.slice(32, 64));
        if (S >= L) {
          return false;
        }
        const k = mod(bytesToBigIntLE(sha512(concatBytes(RBytes, pub, msg))), L);
        const lhs = pointMul(POINT_G, S);
        const rhs = pointAdd(R, pointMul(A, k));
        return pointEqual(lhs, rhs);
      } catch {
        return false;
      }
    },

    fingerprint(pub) {
      const h = toHex(sha256(pub).slice(0, 6)).toUpperCase();
      return h.slice(0, 4) + '-' + h.slice(4, 8) + '-' + h.slice(8, 12);
    },
  };

  // ---- X25519 (RFC 7748 Montgomery Ladder) ----
  const A24 = 121665n;

  function x25519ScalarMult(kBytes, uBytes) {
    const kArr = new Uint8Array(kBytes);
    kArr[0] &= 248;
    kArr[31] &= 127;
    kArr[31] |= 64;
    const k = bytesToBigIntLE(kArr);

    const uArr = new Uint8Array(uBytes);
    uArr[31] &= 127;
    const u = bytesToBigIntLE(uArr);

    let x1 = u;
    let x2 = 1n;
    let z2 = 0n;
    let x3 = u;
    let z3 = 1n;
    let swap = 0n;

    for (let t = 254; t >= 0; t--) {
      const kt = (k >> BigInt(t)) & 1n;
      swap ^= kt;
      if (swap !== 0n) {
        [x2, x3] = [x3, x2];
        [z2, z3] = [z3, z2];
      }
      swap = kt;

      const A = modP(x2 + z2);
      const AA = modP(A * A);
      const B = modP(x2 - z2);
      const BB = modP(B * B);
      const E = modP(AA - BB);
      const C = modP(x3 + z3);
      const D_m = modP(x3 - z3);
      const DA = modP(D_m * A);
      const CB = modP(C * B);
      const sumDACB = modP(DA + CB);
      const diffDACB = modP(DA - CB);
      x3 = modP(sumDACB * sumDACB);
      z3 = modP(x1 * modP(diffDACB * diffDACB));
      x2 = modP(AA * BB);
      z2 = modP(E * modP(AA + modP(A24 * E)));
    }

    if (swap !== 0n) {
      [x2, x3] = [x3, x2];
      [z2, z3] = [z3, z2];
    }

    const res = modP(x2 * powMod(z2, P - 2n, P));
    return bigIntToBytesLE(res, 32);
  }

  const X25519_BASE = (() => {
    const b = new Uint8Array(32);
    b[0] = 9;
    return b;
  })();

  const x25519 = {
    generateKeypair() {
      const privateKey = randomBytes(32);
      const publicKey = x25519ScalarMult(privateKey, X25519_BASE);
      return { privateKey, publicKey };
    },

    sharedSecret(privateKey, peerPublicKey) {
      if (!(peerPublicKey instanceof Uint8Array) || peerPublicKey.length !== 32) {
        throw new Error('Invalid X25519 peer public key');
      }
      const shared = x25519ScalarMult(privateKey, peerPublicKey);
      let nonzero = 0;
      for (let i = 0; i < shared.length; i++) {
        nonzero |= shared[i];
      }
      if (nonzero === 0) {
        throw new Error('Low-order X25519 peer public key rejected');
      }
      return shared;
    },
  };

  // ---- RFC 9382 SPAKE2 on Edwards25519 ----
  const POINT_M = pointDecode(fromHex('d048032c6ea0b6d697ddc2e86bda85a33adac920f1bf18e1b0c6d166a5cecdaf'));
  const POINT_N = pointDecode(fromHex('d3bfb518f44f3430f29d0c92af503865a1ed3281dc69b35dd868ba85f886c4ab'));

  function appendLengthPrefixed(chunks, elem) {
    chunks.push(u64le(elem.length));
    chunks.push(elem);
  }

  function deriveSpake2W(pin, windowId) {
    const salt = utf8Bytes('pounddeck-spake2-v1:' + windowId);
    const raw = hkdfSha256(utf8Bytes(pin), salt, 'w', 64);
    return mod(bytesToBigIntLE(raw), L);
  }

  function randomNonZeroScalar() {
    while (true) {
      const uniform = randomBytes(64);
      const sc = mod(bytesToBigIntLE(uniform), L);
      if (sc !== 0n) {
        return sc;
      }
    }
  }

  const spake2 = {
    clientStart(pin, windowId) {
      const w = deriveSpake2W(pin, windowId);
      const x = randomNonZeroScalar();
      const xG = pointMul(POINT_G, x);
      const wM = pointMul(POINT_M, w);
      const pA = pointEncode(pointAdd(xG, wM));
      return { pin, windowId, w, x, pA };
    },

    clientFinish(state, clientPub, hostPub, pB) {
      const pBPoint = pointDecode(pB);
      const wN = pointMul(POINT_N, state.w);
      const t = pointSub(pBPoint, wN);
      const t8 = pointMultByCofactor(t);
      if (pointIsIdentity(t8)) {
        throw new Error('Server SPAKE2 share in small subgroup');
      }
      const K = pointEncode(pointMul(t8, state.x));
      const wBytes = bigIntToBytesLE(state.w, 32);

      const chunks = [];
      appendLengthPrefixed(chunks, utf8Bytes('pounddeck-pair-v1:' + state.windowId));
      appendLengthPrefixed(chunks, clientPub);
      appendLengthPrefixed(chunks, hostPub);
      appendLengthPrefixed(chunks, state.pA);
      appendLengthPrefixed(chunks, pB);
      appendLengthPrefixed(chunks, K);
      appendLengthPrefixed(chunks, wBytes);
      const transcript = concatBytes(...chunks);

      const hashTT = sha256(transcript);
      const ke = hashTT.slice(0, 16);
      const ka = hashTT.slice(16, 32);
      const kc = hkdfSha256(ka, null, 'ConfirmationKeys', 64);
      const kcA = kc.slice(0, 32);
      const kcB = kc.slice(32, 64);
      const cA = hmacSha256(kcA, transcript);
      const cB = hmacSha256(kcB, transcript);

      const sasBytes = hkdfSha256(ke, null, 'PoundDeck-SAS-v1', 4);
      const dv = new DataView(sasBytes.buffer, sasBytes.byteOffset, 4);
      const sasInt = (dv.getUint32(0, false) & 0x7fffffff) % 1000000;
      const sas = String(Math.floor(sasInt / 1000)).padStart(3, '0') + '-' + String(sasInt % 1000).padStart(3, '0');

      return { K, transcript, hashTT, ke, ka, kcA, kcB, cA, cB, sas };
    },

    hostSigPayload(hashTT) {
      return concatBytes(utf8Bytes('pounddeck-pair-host-v1'), hashTT);
    },

    clientSigPayload(hashTT) {
      return concatBytes(utf8Bytes('pounddeck-pair-client-v1'), hashTT);
    },

    pairConfirmMAC(kcA, attemptId, confirm) {
      return hmacSha256(kcA, utf8Bytes('confirm:' + attemptId + ':' + (confirm ? '1' : '0')));
    },

    pairStatusReqMAC(kcA, attemptId) {
      return hmacSha256(kcA, utf8Bytes('status:' + attemptId));
    },

    pairStatusRespMAC(kcB, attemptId, status, deviceId) {
      return hmacSha256(kcB, utf8Bytes('result:' + attemptId + ':' + status + ':' + (deviceId || '')));
    },
  };

  // ---- WebSocket Mutual Auth & Frame Authentication ----
  const wsAuth = {
    buildTranscript1(hostId, deviceId, hostPub, clientPub, serverNonce, clientNonce, clientEphPub) {
      const chunks = [utf8Bytes('pounddeck-ws-v2')];
      appendLengthPrefixed(chunks, utf8Bytes(hostId));
      appendLengthPrefixed(chunks, utf8Bytes(deviceId));
      chunks.push(hostPub, clientPub, serverNonce, clientNonce, clientEphPub);
      return concatBytes(...chunks);
    },

    buildTranscript2(t1, serverEphPub) {
      return concatBytes(t1, serverEphPub);
    },

    deriveSessionKeys(sharedSecret, t2) {
      const salt = sha256(t2);
      const okm = hkdfSha256(sharedSecret, salt, 'pounddeck-ws-keys-v2', 96);
      return {
        clientMacKey: okm.slice(0, 32),
        serverMacKey: okm.slice(32, 64),
        assetMacKey: okm.slice(64, 96),
      };
    },

    frameMAC(macKey, dir, seq, payload) {
      return hmacSha256(macKey, utf8Bytes(dir + ':' + String(seq) + ':' + payload));
    },

    assetMAC(assetMacKey, sessionId, assetId) {
      return toBase64Url(hmacSha256(assetMacKey, utf8Bytes('asset:' + sessionId + ':' + assetId)));
    },
  };

  return {
    randomBytes,
    utf8Bytes,
    concatBytes,
    constantTimeEqual,
    toBase64,
    fromBase64,
    toBase64Url,
    fromHex,
    toHex,
    sha256,
    sha512,
    hmacSha256,
    hkdfSha256,
    ed25519,
    x25519,
    spake2,
    wsAuth,
  };
});
