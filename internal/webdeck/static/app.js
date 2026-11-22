(() => {
  'use strict';

  const C = window.PoundDeckCrypto;
  const HEX_RE = /^#[0-9a-fA-F]{3,8}$/;
  function parseHexRGB(hex) {
    if (!HEX_RE.test(hex || '')) return null;
    let s = hex.slice(1);
    if (s.length === 3 || s.length === 4) {
      s = s[0] + s[0] + s[1] + s[1] + s[2] + s[2];
    } else if (s.length >= 6) {
      s = s.slice(0, 6);
    } else {
      return null;
    }
    const r = parseInt(s.slice(0, 2), 16);
    const g = parseInt(s.slice(2, 4), 16);
    const b = parseInt(s.slice(4, 6), 16);
    return Number.isNaN(r + g + b) ? null : [r, g, b];
  }
  function relLum(rgb) {
    const lin = rgb.map((v) => {
      const n = v / 255;
      return n <= 0.04045 ? n / 12.92 : Math.pow((n + 0.055) / 1.055, 2.4);
    });
    return 0.2126 * lin[0] + 0.7152 * lin[1] + 0.0722 * lin[2];
  }
  function contrastFgHex(bgHex) {
    const rgb = parseHexRGB(bgHex);
    if (!rgb) return '';
    return relLum(rgb) > 0.17913 ? '#0A0A0C' : '#FFFFFF';
  }
  function hexContrastRatio(aHex, bHex) {
    const a = parseHexRGB(aHex);
    const b = parseHexRGB(bHex);
    if (!a || !b) return 21;
    const la = relLum(a);
    const lb = relLum(b);
    return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
  }
  const nativeBridge =
    window.PoundDeckNative && typeof window.PoundDeckNative.isNativeApp === 'function'
      ? window.PoundDeckNative
      : null;
  const supportsWakeLock = Boolean(!nativeBridge && window.isSecureContext && 'wakeLock' in navigator);
  const supportsFullscreen = Boolean(
    !nativeBridge &&
    document.fullscreenEnabled &&
    document.documentElement &&
    typeof document.documentElement.requestFullscreen === 'function'
  );

  const els = {
    pairHeader: document.getElementById('pair-header'),
    pairConnBadge: document.getElementById('pair-conn-badge'),
    connBadge: document.getElementById('conn-badge'),
    connLabel: document.getElementById('conn-label'),
    profileSelect: document.getElementById('profile-select'),
    nativeOrientWrap: document.getElementById('native-orient-wrap'),
    nativeOrientSelect: document.getElementById('native-orient-select'),
    nativeScanBtn: document.getElementById('native-scan-btn'),
    nativeSetupBtn: document.getElementById('native-setup-btn'),
    nativeScanPairBtn: document.getElementById('native-scan-pair-btn'),
    backFolderBtn: document.getElementById('back-folder-btn'),
    menuBtn: document.getElementById('menu-btn'),
    menuCloseBtn: document.getElementById('menu-close-btn'),
    deckMenu: document.getElementById('deck-menu'),
    wakeBtn: document.getElementById('wake-btn'),
    fullscreenBtn: document.getElementById('fullscreen-btn'),
    logoutBtn: document.getElementById('logout-btn'),
    pairView: document.getElementById('pair-view'),
    pairStepEntry: document.getElementById('pair-step-entry'),
    pairWindowHint: document.getElementById('pair-window-hint'),
    pairForm: document.getElementById('pair-form'),
    deviceNameInput: document.getElementById('device-name-input'),
    pinInput: document.getElementById('pin-input'),
    pairError: document.getElementById('pair-error'),
    pairSubmitBtn: document.getElementById('pair-submit-btn'),
    pinClearBtn: document.getElementById('pin-clear-btn'),
    pinBackBtn: document.getElementById('pin-back-btn'),
    confirmView: document.getElementById('confirm-view'),
    sasCode: document.getElementById('sas-code'),
    confirmHostName: document.getElementById('confirm-host-name'),
    confirmHostFp: document.getElementById('confirm-host-fp'),
    confirmClientFp: document.getElementById('confirm-client-fp'),
    confirmStatus: document.getElementById('confirm-status'),
    confirmError: document.getElementById('confirm-error'),
    confirmApproveBtn: document.getElementById('confirm-approve-btn'),
    confirmCancelBtn: document.getElementById('confirm-cancel-btn'),
    deckView: document.getElementById('deck-view'),
    folderBanner: document.getElementById('folder-banner'),
    keyGrid: document.getElementById('key-grid'),
    dialStage: document.getElementById('dial-stage'),
    dialStrip: document.getElementById('dial-strip'),
  };

  let ws = null;
  let wsSession = null;
  let reconnectTimer = null;
  let heartbeatTimer = null;
  let pairPollTimer = null;
  let reconnectDelayMs = 600;
  let shouldReconnect = false;
  let wakeLock = null;
  let lastSnapshot = null;
  let expectedHostFp = '';
  let activePair = null;

  function storageKey() {
    return 'pounddeck_auth_v2_' + window.location.origin;
  }

  function loadAuthRecord() {
    let raw = '';
    if (nativeBridge && typeof nativeBridge.getAuthRecord === 'function') {
      try {
        raw = nativeBridge.getAuthRecord(window.location.origin) || '';
      } catch {
        raw = '';
      }
    }
    if (!raw) {
      try {
        raw = window.localStorage.getItem(storageKey()) || '';
      } catch {
        raw = '';
      }
    }
    if (!raw) {
      return null;
    }
    try {
      const rec = JSON.parse(raw);
      if (
        !rec ||
        typeof rec.hostId !== 'string' ||
        typeof rec.hostPublicKey !== 'string' ||
        typeof rec.deviceId !== 'string' ||
        typeof rec.clientSeed !== 'string' ||
        typeof rec.clientPublicKey !== 'string'
      ) {
        return null;
      }
      return rec;
    } catch {
      return null;
    }
  }

  function saveAuthRecord(rec) {
    const serialized = JSON.stringify(rec);
    if (nativeBridge && typeof nativeBridge.saveAuthRecord === 'function') {
      try {
        nativeBridge.saveAuthRecord(window.location.origin, serialized);
      } catch {
        // Fall through to localStorage fallback.
      }
    }
    try {
      window.localStorage.setItem(storageKey(), serialized);
    } catch {
      // Ignore restricted storage modes.
    }
  }

  function clearAuthRecord() {
    if (nativeBridge && typeof nativeBridge.clearAuthRecord === 'function') {
      try {
        nativeBridge.clearAuthRecord(window.location.origin);
      } catch {
        // Ignore bridge failure.
      }
    }
    try {
      window.localStorage.removeItem(storageKey());
    } catch {
      // Ignore storage error.
    }
  }

  function defaultDeviceName() {
    if (nativeBridge && typeof nativeBridge.getDeviceName === 'function') {
      try {
        const nativeName = (nativeBridge.getDeviceName() || '').trim();
        if (nativeName) {
          return nativeName.slice(0, 48);
        }
      } catch {
        // Ignore bridge failure.
      }
    }
    const ua = navigator.userAgent || '';
    if (/iPad/i.test(ua)) return 'iPad Browser';
    if (/iPhone/i.test(ua)) return 'iPhone Browser';
    if (/Android/i.test(ua)) return 'Android Browser';
    return 'Web Touch Deck';
  }

  function setStatusBadge(state, label) {
    if (els.connBadge) {
      els.connBadge.dataset.state = state;
    }
    if (els.connLabel) {
      els.connLabel.textContent = label;
    }
    if (els.pairConnBadge) {
      els.pairConnBadge.dataset.state = state;
      els.pairConnBadge.textContent = label;
    }
  }

  function setMenuOpen(open) {
    els.deckMenu.hidden = !open;
    els.menuBtn.setAttribute('aria-expanded', String(open));
  }

  function stopPairPoll() {
    if (pairPollTimer) {
      clearInterval(pairPollTimer);
      pairPollTimer = null;
    }
  }

  function showConfirmView(pairCtx) {
    activePair = pairCtx;
    els.pairStepEntry.hidden = true;
    els.confirmView.hidden = false;
    els.confirmError.hidden = true;
    els.confirmApproveBtn.disabled = false;
    els.sasCode.textContent = pairCtx.mat.sas;
    els.confirmHostName.textContent = pairCtx.info.hostName || 'PoundDeck Studio';
    els.confirmHostFp.textContent = pairCtx.info.hostFingerprint;
    els.confirmClientFp.textContent = C.ed25519.fingerprint(pairCtx.clientPub);
    els.confirmStatus.textContent = 'Check the verification code on your PoundDeck PC and tap Confirm Match.';
    setStatusBadge('connecting', 'Verify Both Devices');
  }

  function showPairView(errorMsg) {
    shouldReconnect = false;
    stopHeartbeat();
    stopPairPoll();
    activePair = null;
    clearTimeout(reconnectTimer);
    wsSession = null;
    if (ws) {
      ws.close();
      ws = null;
    }
    setMenuOpen(false);
    document.body.dataset.view = 'pair';
    els.pairHeader.hidden = false;
    els.deckView.hidden = true;
    els.pairView.hidden = false;
    els.pairStepEntry.hidden = false;
    els.confirmView.hidden = true;
    els.confirmError.hidden = true;
    setStatusBadge('offline', 'Not Paired');

    if (els.deviceNameInput && !els.deviceNameInput.value.trim()) {
      els.deviceNameInput.value = defaultDeviceName();
    }
    if (errorMsg) {
      els.pairError.textContent = errorMsg;
      els.pairError.hidden = false;
    } else {
      els.pairError.hidden = true;
    }
    refreshPairInfo();
  }

  function showDeckView() {
    stopPairPoll();
    activePair = null;
    document.body.dataset.view = 'deck';
    els.pairError.hidden = true;
    els.confirmError.hidden = true;
    els.pairHeader.hidden = true;
    els.pairView.hidden = true;
    els.deckView.hidden = false;
  }

  async function refreshPairInfo() {
    if (!els.pairWindowHint) {
      return null;
    }
    try {
      const res = await fetch('/api/pair/info', { cache: 'no-store' });
      if (!res.ok) {
        return null;
      }
      const info = await res.json();
      if (info.pairingOpen) {
        els.pairWindowHint.dataset.open = 'true';
        els.pairWindowHint.textContent = 'Pairing window open on ' + (info.hostName || 'PC') + ' (Key ' + (info.hostFingerprint || '') + ').';
      } else {
        els.pairWindowHint.dataset.open = 'false';
        els.pairWindowHint.textContent = 'Pairing is currently closed on ' + (info.hostName || 'PC') + '. Click Pair New Device in Settings.';
      }
      els.pairWindowHint.hidden = false;
      return info;
    } catch {
      els.pairWindowHint.hidden = true;
      return null;
    }
  }

  function sendCmd(cmd) {
    if (!ws || ws.readyState !== WebSocket.OPEN || !wsSession) {
      return;
    }
    wsSession.txSeq += 1;
    const payload = JSON.stringify(cmd);
    const mac = C.toBase64(C.wsAuth.frameMAC(wsSession.clientMacKey, 'c2s', wsSession.txSeq, payload));
    ws.send(JSON.stringify({
      seq: wsSession.txSeq,
      payload,
      mac,
    }));
  }

  function startHeartbeat() {
    stopHeartbeat();
    heartbeatTimer = setInterval(() => {
      sendCmd({ type: 'ping' });
    }, 25000);
  }

  function stopHeartbeat() {
    if (heartbeatTimer) {
      clearInterval(heartbeatTimer);
      heartbeatTimer = null;
    }
  }

  function connectWS() {
    const auth = loadAuthRecord();
    if (!auth) {
      showPairView('');
      return;
    }

    clearTimeout(reconnectTimer);
    shouldReconnect = true;
    wsSession = null;
    showDeckView();
    setStatusBadge('connecting', 'Authenticating...');

    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const url = proto + '//' + window.location.host + '/ws';
    const socket = new WebSocket(url);
    ws = socket;

    let handshaked = false;
    let pendingHandshake = null;

    socket.addEventListener('message', (ev) => {
      if (ws !== socket) {
        return;
      }
      let msg;
      try {
        msg = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (!msg) {
        return;
      }

      if (!handshaked) {
        if (msg.type === 'auth_error') {
          shouldReconnect = false;
          clearAuthRecord();
          socket.close();
          showPairView(msg.message || 'Device pairing revoked on host. Re-pair to continue.');
          return;
        }
        if (msg.type === 'auth_hello') {
          if (msg.hostId !== auth.hostId || msg.hostPublicKey !== auth.hostPublicKey) {
            shouldReconnect = false;
            socket.close();
            showPairView('Security alert: Host public key mismatch. Re-pair if this PC was reset.');
            return;
          }
          try {
            const hostPub = C.fromBase64(auth.hostPublicKey);
            const clientSeed = C.fromBase64(auth.clientSeed);
            const clientPub = C.fromBase64(auth.clientPublicKey);
            const serverNonce = C.fromBase64(msg.serverNonce);
            const clientNonce = C.randomBytes(32);
            const eph = C.x25519.generateKeypair();
            const t1 = C.wsAuth.buildTranscript1(
              auth.hostId,
              auth.deviceId,
              hostPub,
              clientPub,
              serverNonce,
              clientNonce,
              eph.publicKey
            );
            const clientSig = C.ed25519.sign(clientSeed, t1);
            pendingHandshake = { hostPub, t1, ephPriv: eph.privateKey };
            socket.send(JSON.stringify({
              type: 'auth_response',
              deviceId: auth.deviceId,
              clientNonce: C.toBase64(clientNonce),
              clientEphPub: C.toBase64(eph.publicKey),
              clientSig: C.toBase64(clientSig),
            }));
          } catch {
            shouldReconnect = false;
            socket.close();
            showPairView('Client cryptographic identity error. Please re-pair.');
          }
          return;
        }
        if (msg.type === 'auth_ok' && pendingHandshake) {
          try {
            const serverEphPub = C.fromBase64(msg.serverEphPub);
            const hostSig = C.fromBase64(msg.hostSig);
            const t2 = C.wsAuth.buildTranscript2(pendingHandshake.t1, serverEphPub);
            if (!C.ed25519.verify(pendingHandshake.hostPub, t2, hostSig)) {
              shouldReconnect = false;
              socket.close();
              showPairView('Security alert: Host authentication signature failed.');
              return;
            }
            const shared = C.x25519.sharedSecret(pendingHandshake.ephPriv, serverEphPub);
            const keys = C.wsAuth.deriveSessionKeys(shared, t2);
            wsSession = {
              sessionId: msg.sessionId,
              clientMacKey: keys.clientMacKey,
              serverMacKey: keys.serverMacKey,
              assetMacKey: keys.assetMacKey,
              txSeq: 0,
              rxSeq: 0,
            };
            handshaked = true;
            pendingHandshake = null;
            reconnectDelayMs = 600;
            setStatusBadge('online', 'Live');
            startHeartbeat();
          } catch {
            shouldReconnect = false;
            socket.close();
            showPairView('Host key exchange failed.');
          }
          return;
        }
        return;
      }

      // Signed post-handshake frame verification.
      if (
        !wsSession ||
        typeof msg.seq !== 'number' ||
        msg.seq <= wsSession.rxSeq ||
        typeof msg.payload !== 'string' ||
        typeof msg.mac !== 'string'
      ) {
        shouldReconnect = false;
        socket.close();
        showPairView('Security alert: Invalid or replayed host frame rejected.');
        return;
      }
      const expectedMac = C.wsAuth.frameMAC(wsSession.serverMacKey, 's2c', msg.seq, msg.payload);
      if (!C.constantTimeEqual(C.fromBase64(msg.mac), expectedMac)) {
        shouldReconnect = false;
        socket.close();
        showPairView('Security alert: Host frame MAC verification failed.');
        return;
      }
      wsSession.rxSeq = msg.seq;

      let inner;
      try {
        inner = JSON.parse(msg.payload);
      } catch {
        return;
      }
      if (!inner || typeof inner.type !== 'string') {
        return;
      }
      if (inner.type === 'snapshot' && inner.snapshot) {
        lastSnapshot = inner.snapshot;
        renderSnapshot(inner.snapshot);
      } else if (inner.type === 'auth_revoked') {
        shouldReconnect = false;
        clearAuthRecord();
        showPairView(inner.message || 'Device access revoked on host. Re-pair to continue.');
      }
    });

    socket.addEventListener('close', () => {
      stopHeartbeat();
      if (ws === socket) {
        ws = null;
        wsSession = null;
      }
      if (!shouldReconnect) {
        return;
      }
      setStatusBadge('offline', 'Reconnecting...');
      reconnectTimer = setTimeout(connectWS, reconnectDelayMs);
      reconnectDelayMs = Math.min(5000, Math.round(reconnectDelayMs * 1.5));
    });
  }

  function renderSnapshot(snap) {
    const cols = Math.max(1, snap.grid?.cols || 5);
    const rows = Math.max(1, snap.grid?.rows || 3);
    document.documentElement.style.setProperty('--cols', String(cols));
    document.documentElement.style.setProperty('--rows', String(rows));

    els.profileSelect.replaceChildren();
    for (const p of snap.profiles || []) {
      const opt = document.createElement('option');
      opt.value = p.id;
      opt.textContent = p.name;
      if (p.id === snap.activeProfileId) {
        opt.selected = true;
      }
      els.profileSelect.appendChild(opt);
    }

    els.backFolderBtn.hidden = !snap.inFolder;

    if (snap.inFolder && Array.isArray(snap.folderPath) && snap.folderPath.length > 0) {
      els.folderBanner.textContent = 'Folder: ' + snap.folderPath.join(' / ');
      els.folderBanner.hidden = false;
    } else {
      els.folderBanner.hidden = true;
    }

    const keyNodes = [];
    const slots = snap.slots || {};
    for (let r = 0; r < rows; r++) {
      for (let c = 0; c < cols; c++) {
        const slot = slots[r + ',' + c] || null;
        keyNodes.push(buildKeyTile(r, c, slot));
      }
    }
    els.keyGrid.replaceChildren(...keyNodes);

    const dialCount = snap.dials || 0;
    if (dialCount > 0 && Array.isArray(snap.dialSlots)) {
      const dialNodes = snap.dialSlots.map((d) => buildDialCard(d));
      els.dialStrip.replaceChildren(...dialNodes);
      els.dialStage.hidden = false;
    } else {
      els.dialStage.hidden = true;
      els.dialStrip.replaceChildren();
    }
  }

  function buildKeyTile(row, col, slot) {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'key-tile';

    const assigned = Boolean(slot && slot.assigned);
    const autoBack = Boolean(slot && slot.autoBack);
    const title = slot ? (slot.title || '') : '';
    const subtitle = slot ? (slot.subtitle || '') : '';
    const imageUrl = slot ? (slot.imageUrl || '') : '';
    const bgImageUrl = slot ? (slot.bgImageUrl || '') : '';
    const flash = slot ? (slot.flash || '') : '';
    const color = slot && HEX_RE.test(slot.color || '') ? slot.color : '';
    const bgColor = slot && HEX_RE.test(slot.bgColor || '') ? slot.bgColor : '';

    btn.dataset.assigned = String(assigned);
    btn.dataset.autoback = String(autoBack);
    if (flash) {
      btn.dataset.flash = flash;
    }
    if (color) {
      btn.style.setProperty('--tile-accent', color);
    }
    if (bgColor && (assigned || autoBack)) {
      btn.dataset.hasBgColor = 'true';
      btn.style.setProperty('--tile-bg', bgColor);
      const fg = contrastFgHex(bgColor);
      if (fg) {
        btn.style.setProperty('--tile-fg', fg);
      }
      const accentEff = color || '#38BDF8';
      if (hexContrastRatio(accentEff, bgColor) < 3.0 && fg) {
        btn.style.setProperty('--tile-sub-fg', fg);
      }
    }
    if (bgImageUrl && (assigned || autoBack)) {
      btn.dataset.hasBgImage = 'true';
    }

    const fullArt = Boolean((imageUrl || bgImageUrl) && title.trim() === '' && subtitle.trim() === '');
    if (fullArt) {
      btn.dataset.fullart = 'true';
    }

    const ariaLabel = title || subtitle || (assigned ? ('Action key row ' + (row + 1) + ' column ' + (col + 1)) : ('Empty key row ' + (row + 1) + ' column ' + (col + 1)));
    btn.setAttribute('aria-label', ariaLabel);

    if (bgImageUrl && (assigned || autoBack)) {
      const bgImg = document.createElement('img');
      bgImg.className = 'key-bg-img';
      bgImg.alt = '';
      bgImg.draggable = false;
      bgImg.src = bgImageUrl;
      btn.appendChild(bgImg);
    }

    if (!fullArt && (assigned || autoBack)) {
      const bar = document.createElement('span');
      bar.className = 'key-accent-bar';
      bar.setAttribute('aria-hidden', 'true');
      btn.appendChild(bar);
    }

    if (imageUrl) {
      const img = document.createElement('img');
      img.className = 'key-art';
      img.alt = '';
      img.draggable = false;
      img.src = imageUrl;
      btn.appendChild(img);
    }

    if (!fullArt) {
      if (assigned || autoBack) {
        const titleEl = document.createElement('span');
        titleEl.className = 'key-title';
        titleEl.textContent = title || 'Action';
        btn.appendChild(titleEl);

        if (subtitle) {
          const subEl = document.createElement('span');
          subEl.className = 'key-subtitle';
          subEl.textContent = subtitle;
          btn.appendChild(subEl);
        }
      } else {
        const emptyEl = document.createElement('span');
        emptyEl.className = 'key-empty-hint';
        emptyEl.textContent = '(' + row + ', ' + col + ')';
        btn.appendChild(emptyEl);
      }
    }

    let activePointerId = null;
    btn.addEventListener('pointerdown', (ev) => {
      if (ev.button !== 0 && ev.pointerType === 'mouse') {
        return;
      }
      activePointerId = ev.pointerId;
      try {
        btn.setPointerCapture(ev.pointerId);
      } catch {
        // Ignore if capture is unsupported.
      }
      btn.classList.add('is-pressed');
      if (navigator.vibrate) {
        navigator.vibrate(12);
      }
      sendCmd({ type: 'keyDown', row, col });
    });

    const release = (ev) => {
      if (activePointerId !== ev.pointerId) {
        return;
      }
      activePointerId = null;
      btn.classList.remove('is-pressed');
      sendCmd({ type: 'keyUp', row, col });
    };
    btn.addEventListener('pointerup', release);
    btn.addEventListener('pointercancel', release);

    btn.addEventListener('keydown', (ev) => {
      if (ev.repeat) {
        return;
      }
      if (ev.key === 'Enter' || ev.key === ' ') {
        ev.preventDefault();
        btn.classList.add('is-pressed');
        sendCmd({ type: 'keyDown', row, col });
      }
    });
    btn.addEventListener('keyup', (ev) => {
      if (ev.key === 'Enter' || ev.key === ' ') {
        ev.preventDefault();
        btn.classList.remove('is-pressed');
        sendCmd({ type: 'keyUp', row, col });
      }
    });

    return btn;
  }

  function buildDialCard(dial) {
    const card = document.createElement('div');
    card.className = 'dial-card';
    const dialAccent = dial.color && HEX_RE.test(dial.color) ? dial.color : '';
    const dialBg = dial.assigned && dial.bgColor && HEX_RE.test(dial.bgColor) ? dial.bgColor : '';
    if (dialAccent) {
      card.style.setProperty('--dial-accent', dialAccent);
    }

    const face = document.createElement('div');
    face.className = 'dial-face';
    face.title = 'Drag left or right to rotate';
    if (dialBg) {
      face.dataset.hasBgColor = 'true';
      face.style.setProperty('--dial-bg', dialBg);
      const fg = contrastFgHex(dialBg);
      if (fg) {
        face.style.setProperty('--dial-fg', fg);
      }
      if (hexContrastRatio(dialAccent || '#38BDF8', dialBg) < 3.0 && fg) {
        face.style.setProperty('--dial-sub-fg', fg);
      }
    }

    const titleEl = document.createElement('span');
    titleEl.className = 'dial-title';
    titleEl.textContent = dial.title || ('Dial ' + (dial.index + 1));
    face.appendChild(titleEl);

    const valEl = document.createElement('span');
    valEl.className = 'dial-value';
    valEl.textContent = dial.value || 'Empty';
    face.appendChild(valEl);

    if (dial.hasIndicator) {
      const bar = document.createElement('div');
      bar.className = 'dial-bar';
      const fill = document.createElement('div');
      fill.className = 'dial-bar-fill';
      const pct = Math.max(0, Math.min(100, Number(dial.indicatorPct) || 0));
      fill.style.width = pct + '%';
      bar.appendChild(fill);
      face.appendChild(bar);
    }

    let dragPointer = null;
    let accumX = 0;
    let lastX = 0;
    face.addEventListener('pointerdown', (ev) => {
      dragPointer = ev.pointerId;
      lastX = ev.clientX;
      accumX = 0;
      try {
        face.setPointerCapture(ev.pointerId);
      } catch {
        // Ignore if unsupported.
      }
    });
    face.addEventListener('pointermove', (ev) => {
      if (dragPointer !== ev.pointerId) {
        return;
      }
      accumX += ev.clientX - lastX;
      lastX = ev.clientX;
      const stepPx = 18;
      const ticks = Math.trunc(accumX / stepPx);
      if (ticks !== 0) {
        accumX -= ticks * stepPx;
        sendCmd({ type: 'dialRotate', dial: dial.index, ticks });
      }
    });
    const endDrag = (ev) => {
      if (dragPointer === ev.pointerId) {
        dragPointer = null;
      }
    };
    face.addEventListener('pointerup', endDrag);
    face.addEventListener('pointercancel', endDrag);

    const controls = document.createElement('div');
    controls.className = 'dial-buttons';

    const minusBtn = document.createElement('button');
    minusBtn.type = 'button';
    minusBtn.className = 'dial-step-btn';
    minusBtn.textContent = '-';
    minusBtn.setAttribute('aria-label', (dial.title || 'Dial ' + (dial.index + 1)) + ' decrement');
    minusBtn.addEventListener('click', () => sendCmd({ type: 'dialRotate', dial: dial.index, ticks: -1 }));

    const pressBtn = document.createElement('button');
    pressBtn.type = 'button';
    pressBtn.className = 'dial-step-btn';
    pressBtn.textContent = 'Press';
    pressBtn.setAttribute('aria-label', (dial.title || 'Dial ' + (dial.index + 1)) + ' press');
    pressBtn.addEventListener('click', () => sendCmd({ type: 'dialPress', dial: dial.index }));

    const plusBtn = document.createElement('button');
    plusBtn.type = 'button';
    plusBtn.className = 'dial-step-btn';
    plusBtn.textContent = '+';
    plusBtn.setAttribute('aria-label', (dial.title || 'Dial ' + (dial.index + 1)) + ' increment');
    plusBtn.addEventListener('click', () => sendCmd({ type: 'dialRotate', dial: dial.index, ticks: 1 }));

    controls.append(minusBtn, pressBtn, plusBtn);
    card.append(face, controls);
    return card;
  }

  function applyPairStatusUpdate(data) {
    if (!activePair || data.attemptId !== activePair.attemptId) {
      return;
    }
    const expected = C.spake2.pairStatusRespMAC(activePair.mat.kcB, data.attemptId, data.status, data.deviceId);
    if (!C.constantTimeEqual(C.fromBase64(data.mac), expected)) {
      stopPairPoll();
      showPairView('Security alert: Pairing status verification failed.');
      return;
    }

    if (data.status === 'approved' && data.deviceId) {
      stopPairPoll();
      saveAuthRecord({
        hostId: activePair.info.hostId,
        hostName: activePair.info.hostName,
        hostPublicKey: activePair.info.hostPublicKey,
        deviceId: data.deviceId,
        deviceName: activePair.deviceName,
        clientSeed: C.toBase64(activePair.clientSeed),
        clientPublicKey: C.toBase64(activePair.clientPub),
      });
      expectedHostFp = '';
      els.pinInput.value = '';
      connectWS();
      return;
    }

    if (data.status === 'denied' || data.status === 'expired') {
      stopPairPoll();
      const msg = data.status === 'denied'
        ? 'Pairing request was denied on PoundDeck PC.'
        : 'Pairing window expired before confirmation.';
      showPairView(msg);
      return;
    }

    if (data.hostApproved && !activePair.clientConfirmed) {
      els.confirmStatus.textContent = 'PoundDeck PC approved! Tap Confirm Match on this device to finish.';
    } else if (activePair.clientConfirmed && !data.hostApproved) {
      els.confirmStatus.textContent = 'Confirmed on this device — tap Approve Device on your PoundDeck PC to finish.';
    }
  }

  function startPairPolling(pairCtx) {
    stopPairPoll();
    pairPollTimer = setInterval(async () => {
      if (!activePair || activePair.attemptId !== pairCtx.attemptId) {
        stopPairPoll();
        return;
      }
      try {
        const proof = C.toBase64(C.spake2.pairStatusReqMAC(pairCtx.mat.kcA, pairCtx.attemptId));
        const res = await fetch('/api/pair/status/' + encodeURIComponent(pairCtx.attemptId), {
          cache: 'no-store',
          headers: { 'X-Pair-Proof': proof },
        });
        if (!res.ok) {
          return;
        }
        const data = await res.json();
        applyPairStatusUpdate(data);
      } catch {
        // Ignore transient LAN poll failure until expiry.
      }
    }, 750);
  }

  // Pairing keypad & SPAKE2 submission
  document.querySelectorAll('.digit-btn[data-digit]').forEach((btn) => {
    btn.addEventListener('click', () => {
      if (els.pinInput.value.length < 6) {
        els.pinInput.value += btn.dataset.digit;
      }
    });
  });

  els.pinClearBtn.addEventListener('click', () => {
    els.pinInput.value = '';
    els.pairError.hidden = true;
  });

  els.pinBackBtn.addEventListener('click', () => {
    els.pinInput.value = els.pinInput.value.slice(0, -1);
  });

  els.pairForm.addEventListener('submit', async (ev) => {
    ev.preventDefault();
    const pin = els.pinInput.value.trim();
    if (!/^[0-9]{6}$/.test(pin)) {
      els.pairError.textContent = 'Please enter a valid 6-digit numeric PIN.';
      els.pairError.hidden = false;
      return;
    }
    const deviceName = (els.deviceNameInput?.value || '').trim() || defaultDeviceName();

    els.pairError.hidden = true;
    els.pairSubmitBtn.disabled = true;
    try {
      const info = await refreshPairInfo();
      if (!info) {
        throw new Error('Could not reach PoundDeck server.');
      }
      if (!info.pairingOpen || !info.windowId) {
        throw new Error('Pairing is closed on PoundDeck PC. Click Pair New Device in Settings.');
      }

      const hostPub = C.fromBase64(info.hostPublicKey);
      const computedFp = C.ed25519.fingerprint(hostPub);
      if (computedFp !== info.hostFingerprint) {
        throw new Error('Server key fingerprint verification failed.');
      }
      if (expectedHostFp && computedFp.toUpperCase() !== expectedHostFp.toUpperCase()) {
        throw new Error('Host fingerprint does not match the scanned QR code.');
      }

      const kp = C.ed25519.generateKeypair();
      const clientPub = kp.publicKey;
      const clientSeed = kp.seed;
      const st = C.spake2.clientStart(pin, info.windowId);

      const startRes = await fetch('/api/pair/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          windowId: info.windowId,
          deviceName,
          clientPublicKey: C.toBase64(clientPub),
          pA: C.toBase64(st.pA),
        }),
      });
      const startData = await startRes.json().catch(() => ({}));
      if (!startRes.ok) {
        throw new Error(startData.error || 'Pairing initialization failed.');
      }

      const pB = C.fromBase64(startData.pB);
      const mat = C.spake2.clientFinish(st, clientPub, hostPub, pB);
      const clientSig = C.ed25519.sign(clientSeed, C.spake2.clientSigPayload(mat.hashTT));

      const verifyRes = await fetch('/api/pair/verify', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          attemptId: startData.attemptId,
          cA: C.toBase64(mat.cA),
          clientSig: C.toBase64(clientSig),
        }),
      });
      const verifyData = await verifyRes.json().catch(() => ({}));
      if (!verifyRes.ok) {
        throw new Error(verifyData.error || 'Invalid pairing PIN.');
      }
      if (!C.constantTimeEqual(C.fromBase64(verifyData.cB), mat.cB)) {
        throw new Error('Host SPAKE2 confirmation tag mismatch.');
      }
      if (!C.ed25519.verify(hostPub, C.spake2.hostSigPayload(mat.hashTT), C.fromBase64(verifyData.hostSig))) {
        throw new Error('Host identity signature verification failed.');
      }

      const pairCtx = {
        attemptId: startData.attemptId,
        info,
        hostPub,
        clientSeed,
        clientPub,
        deviceName,
        mat,
        clientConfirmed: false,
      };
      showConfirmView(pairCtx);
      startPairPolling(pairCtx);
    } catch (err) {
      els.pairError.textContent = err && err.message ? err.message : 'Pairing failed.';
      els.pairError.hidden = false;
    } finally {
      els.pairSubmitBtn.disabled = false;
    }
  });

  els.confirmApproveBtn.addEventListener('click', async () => {
    if (!activePair) {
      return;
    }
    const pairCtx = activePair;
    els.confirmError.hidden = true;
    els.confirmApproveBtn.disabled = true;
    try {
      const mac = C.toBase64(C.spake2.pairConfirmMAC(pairCtx.mat.kcA, pairCtx.attemptId, true));
      const res = await fetch('/api/pair/confirm', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          attemptId: pairCtx.attemptId,
          confirm: true,
          mac,
        }),
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) {
        throw new Error(data.error || 'Confirmation failed.');
      }
      pairCtx.clientConfirmed = true;
      applyPairStatusUpdate(data);
    } catch (err) {
      els.confirmApproveBtn.disabled = false;
      els.confirmError.textContent = err && err.message ? err.message : 'Could not confirm pairing.';
      els.confirmError.hidden = false;
    }
  });

  els.confirmCancelBtn.addEventListener('click', async () => {
    const pairCtx = activePair;
    stopPairPoll();
    if (pairCtx) {
      try {
        const mac = C.toBase64(C.spake2.pairConfirmMAC(pairCtx.mat.kcA, pairCtx.attemptId, false));
        await fetch('/api/pair/confirm', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            attemptId: pairCtx.attemptId,
            confirm: false,
            mac,
          }),
        });
      } catch {
        // Ignore network failure during cancel.
      }
    }
    showPairView('');
  });

  // Compact deck controls & floating menu
  els.menuBtn.addEventListener('click', (ev) => {
    ev.stopPropagation();
    setMenuOpen(els.deckMenu.hidden);
  });

  els.menuCloseBtn.addEventListener('click', () => setMenuOpen(false));

  document.addEventListener('click', (ev) => {
    if (!els.deckMenu.hidden && !els.deckMenu.contains(ev.target) && !els.menuBtn.contains(ev.target)) {
      setMenuOpen(false);
    }
  });

  document.addEventListener('keydown', (ev) => {
    if (ev.key === 'Escape' && !els.deckMenu.hidden) {
      setMenuOpen(false);
      els.menuBtn.focus();
    }
  });

  els.profileSelect.addEventListener('change', () => {
    if (lastSnapshot && els.profileSelect.value !== lastSnapshot.activeProfileId) {
      sendCmd({ type: 'switchProfile', profileId: els.profileSelect.value });
      setMenuOpen(false);
    }
  });

  els.backFolderBtn.addEventListener('click', () => sendCmd({ type: 'closeFolder' }));

  els.wakeBtn.hidden = !supportsWakeLock;
  if (supportsWakeLock) {
    els.wakeBtn.addEventListener('click', async () => {
      if (wakeLock) {
        await wakeLock.release();
        wakeLock = null;
        els.wakeBtn.setAttribute('aria-pressed', 'false');
        return;
      }
      try {
        wakeLock = await navigator.wakeLock.request('screen');
        els.wakeBtn.setAttribute('aria-pressed', 'true');
        wakeLock.addEventListener('release', () => {
          wakeLock = null;
          els.wakeBtn.setAttribute('aria-pressed', 'false');
        });
      } catch {
        els.wakeBtn.setAttribute('aria-pressed', 'false');
      }
    });
  }

  els.fullscreenBtn.hidden = !supportsFullscreen;
  if (supportsFullscreen) {
    els.fullscreenBtn.addEventListener('click', async () => {
      try {
        if (!document.fullscreenElement) {
          await document.documentElement.requestFullscreen();
        } else {
          await document.exitFullscreen();
        }
      } catch {
        // Ignore fullscreen denial on restricted mobile browsers.
      } finally {
        setMenuOpen(false);
      }
    });
  }

  els.logoutBtn.addEventListener('click', () => {
    shouldReconnect = false;
    if (ws && ws.readyState === WebSocket.OPEN && wsSession) {
      sendCmd({ type: 'unpair' });
    }
    clearAuthRecord();
    showPairView('');
  });

  if (nativeBridge) {
    els.nativeOrientWrap.hidden = false;
    els.nativeScanBtn.hidden = false;
    els.nativeSetupBtn.hidden = false;
    els.nativeScanPairBtn.hidden = false;
    try {
      els.nativeOrientSelect.value = nativeBridge.getOrientation() || 'auto';
    } catch {
      els.nativeOrientSelect.value = 'auto';
    }
    els.nativeOrientSelect.addEventListener('change', () => {
      nativeBridge.setOrientation(els.nativeOrientSelect.value);
      setMenuOpen(false);
    });
    els.nativeScanBtn.addEventListener('click', () => {
      setMenuOpen(false);
      nativeBridge.openScanner();
    });
    els.nativeSetupBtn.addEventListener('click', () => {
      setMenuOpen(false);
      nativeBridge.openSetup();
    });
    els.nativeScanPairBtn.addEventListener('click', () => {
      nativeBridge.openScanner();
    });
  }

  function handleHashPair() {
    if (!window.location.hash || window.location.hash.length <= 1) {
      return false;
    }
    const hashParams = new URLSearchParams(window.location.hash.slice(1));
    const qrPin = (hashParams.get('pair') || '').trim();
    const qrFp = (hashParams.get('fp') || '').trim();
    try {
      window.history.replaceState(null, '', window.location.pathname + window.location.search);
    } catch {
      // Ignore history restriction.
    }
    if (/^[0-9]{6}$/.test(qrPin)) {
      expectedHostFp = qrFp;
      showPairView('');
      els.pinInput.value = qrPin;
      return true;
    }
    return false;
  }

  window.addEventListener('hashchange', handleHashPair);

  function init() {
    if (handleHashPair()) {
      return;
    }

    const auth = loadAuthRecord();
    if (auth) {
      connectWS();
      return;
    }
    showPairView('');
  }

  init();
})();
