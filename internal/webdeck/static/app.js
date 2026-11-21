(() => {
  'use strict';

  const HEX_RE = /^#[0-9a-fA-F]{3,8}$/;
  const supportsWakeLock = Boolean(window.isSecureContext && 'wakeLock' in navigator);
  const supportsFullscreen = Boolean(
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
    backFolderBtn: document.getElementById('back-folder-btn'),
    menuBtn: document.getElementById('menu-btn'),
    menuCloseBtn: document.getElementById('menu-close-btn'),
    deckMenu: document.getElementById('deck-menu'),
    wakeBtn: document.getElementById('wake-btn'),
    fullscreenBtn: document.getElementById('fullscreen-btn'),
    logoutBtn: document.getElementById('logout-btn'),
    pairView: document.getElementById('pair-view'),
    pairForm: document.getElementById('pair-form'),
    pinInput: document.getElementById('pin-input'),
    pairError: document.getElementById('pair-error'),
    pinClearBtn: document.getElementById('pin-clear-btn'),
    pinBackBtn: document.getElementById('pin-back-btn'),
    deckView: document.getElementById('deck-view'),
    folderBanner: document.getElementById('folder-banner'),
    keyGrid: document.getElementById('key-grid'),
    dialStage: document.getElementById('dial-stage'),
    dialStrip: document.getElementById('dial-strip'),
  };

  let ws = null;
  let reconnectTimer = null;
  let heartbeatTimer = null;
  let reconnectDelayMs = 600;
  let shouldReconnect = false;
  let wakeLock = null;
  let lastSnapshot = null;

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

  function showPairView(errorMsg) {
    shouldReconnect = false;
    stopHeartbeat();
    clearTimeout(reconnectTimer);
    if (ws) {
      ws.close();
      ws = null;
    }
    setMenuOpen(false);
    document.body.dataset.view = 'pair';
    els.pairHeader.hidden = false;
    els.deckView.hidden = true;
    els.pairView.hidden = false;
    setStatusBadge('offline', 'Not Paired');
    if (errorMsg) {
      els.pairError.textContent = errorMsg;
      els.pairError.hidden = false;
    } else {
      els.pairError.hidden = true;
    }
  }

  function showDeckView() {
    document.body.dataset.view = 'deck';
    els.pairError.hidden = true;
    els.pairHeader.hidden = true;
    els.pairView.hidden = true;
    els.deckView.hidden = false;
  }

  function sendCmd(cmd) {
    if (!ws || ws.readyState !== WebSocket.OPEN) {
      return;
    }
    ws.send(JSON.stringify(cmd));
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
    clearTimeout(reconnectTimer);
    shouldReconnect = true;
    showDeckView();
    setStatusBadge('connecting', 'Connecting...');

    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const url = proto + '//' + window.location.host + '/ws';
    const socket = new WebSocket(url);
    ws = socket;

    socket.addEventListener('open', () => {
      reconnectDelayMs = 600;
      setStatusBadge('online', 'Live');
      startHeartbeat();
    });

    socket.addEventListener('message', (ev) => {
      let msg;
      try {
        msg = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (!msg || typeof msg.type !== 'string') {
        return;
      }
      if (msg.type === 'snapshot' && msg.snapshot) {
        lastSnapshot = msg.snapshot;
        renderSnapshot(msg.snapshot);
      } else if (msg.type === 'auth_revoked') {
        showPairView(msg.message || 'Session expired. Enter pairing PIN to reconnect.');
      }
    });

    socket.addEventListener('close', async () => {
      stopHeartbeat();
      if (ws === socket) {
        ws = null;
      }
      if (!shouldReconnect) {
        return;
      }
      setStatusBadge('offline', 'Reconnecting...');
      try {
        const res = await fetch('/api/auth/status', { cache: 'no-store' });
        const data = await res.json();
        if (!data || !data.authenticated) {
          showPairView('Session expired or revoked on host.');
          return;
        }
      } catch {
        // Offline retry below.
      }
      reconnectTimer = setTimeout(connectWS, reconnectDelayMs);
      reconnectDelayMs = Math.min(5000, Math.round(reconnectDelayMs * 1.5));
    });
  }

  function renderSnapshot(snap) {
    const cols = Math.max(1, snap.grid?.cols || 5);
    const rows = Math.max(1, snap.grid?.rows || 3);
    document.documentElement.style.setProperty('--cols', String(cols));
    document.documentElement.style.setProperty('--rows', String(rows));

    // Profiles dropdown
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

    // Keypad tiles
    const keyNodes = [];
    const slots = snap.slots || {};
    for (let r = 0; r < rows; r++) {
      for (let c = 0; c < cols; c++) {
        const slot = slots[r + ',' + c] || null;
        keyNodes.push(buildKeyTile(r, c, slot));
      }
    }
    els.keyGrid.replaceChildren(...keyNodes);

    // Virtual encoder dials
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
    const flash = slot ? (slot.flash || '') : '';
    const color = slot && HEX_RE.test(slot.color || '') ? slot.color : '';

    btn.dataset.assigned = String(assigned);
    btn.dataset.autoback = String(autoBack);
    if (flash) {
      btn.dataset.flash = flash;
    }
    if (color) {
      btn.style.setProperty('--tile-accent', color);
    }

    const fullArt = Boolean(imageUrl && title.trim() === '' && subtitle.trim() === '');
    if (fullArt) {
      btn.dataset.fullart = 'true';
    }

    const ariaLabel = title || subtitle || (assigned ? ('Action key row ' + (row + 1) + ' column ' + (col + 1)) : ('Empty key row ' + (row + 1) + ' column ' + (col + 1)));
    btn.setAttribute('aria-label', ariaLabel);

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
    if (dial.color && HEX_RE.test(dial.color)) {
      card.style.setProperty('--dial-accent', dial.color);
    }

    const face = document.createElement('div');
    face.className = 'dial-face';
    face.title = 'Drag left or right to rotate';

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

  // Pairing keypad & submission
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
    try {
      const res = await fetch('/api/pair', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pin }),
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) {
        els.pairError.textContent = data.error || 'Pairing failed.';
        els.pairError.hidden = false;
        return;
      }
      els.pinInput.value = '';
      connectWS();
    } catch {
      els.pairError.textContent = 'Could not reach PoundDeck server.';
      els.pairError.hidden = false;
    }
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

  els.logoutBtn.addEventListener('click', async () => {
    try {
      await fetch('/api/logout', { method: 'POST' });
    } finally {
      showPairView('');
    }
  });

  async function init() {
    setStatusBadge('connecting', 'Checking...');
    try {
      const res = await fetch('/api/auth/status', { cache: 'no-store' });
      const data = await res.json();
      if (data && data.authenticated) {
        connectWS();
        return;
      }
    } catch {
      // Fall back to pairing view.
    }
    showPairView('');
  }

  init();
})();
