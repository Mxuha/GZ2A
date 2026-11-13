// ==UserScript==
// @name         GZ2A Token Collector
// @namespace    gz2a
// @version      1.0.0
// @description  Collect fresh z_um device tokens on chat.z.ai for your GZ2A proxy (Android-friendly). Also copies your Z.AI login token to unlock GLM-5 models + vision.
// @author       GZ2A Android package
// @match        https://chat.z.ai/*
// @match        https://chat.z.ai
// @run-at       document-idle
// @grant        none
// ==/UserScript==

(function () {
  'use strict';

  // ── tiny UI ─────────────────────────────────────────────────────────
  var panel = document.createElement('div');
  panel.style.cssText =
    'position:fixed;bottom:16px;right:16px;z-index:2147483647;' +
    'background:#0f172aee;color:#e2e8f0;font:13px/1.45 system-ui,sans-serif;' +
    'border:1px solid #334155;border-radius:12px;padding:12px;width:250px;' +
    'box-shadow:0 8px 30px #000a;backdrop-filter:blur(6px)';
  panel.innerHTML =
    '<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">' +
    '<b style="color:#7dd3fc">GZ2A</b>' +
    '<span data-act="hide" style="cursor:pointer;color:#94a3b8;padding:0 4px">✕</span></div>' +
    '<div data-el="status" style="margin-bottom:8px;color:#94a3b8;min-height:18px"></div>' +
    '<div style="display:flex;gap:6px;margin-bottom:6px">' +
    '<input data-el="count" type="number" min="10" max="1000" step="10" value="100" ' +
    'style="width:70px;background:#1e293b;border:1px solid #334155;color:#e2e8f0;border-radius:6px;padding:4px 6px">' +
    '<button data-act="collect" style="flex:1;background:#0284c7;border:0;color:#fff;border-radius:6px;padding:6px;cursor:pointer;font-weight:600">Collect tokens</button></div>' +
    '<button data-act="zai" style="width:100%;background:#1e293b;border:1px solid #334155;color:#e2e8f0;border-radius:6px;padding:6px;cursor:pointer">Copy Z.AI token</button>';
  document.body.appendChild(panel);
  panel.querySelector('[data-act="hide"]').onclick = function () { panel.remove(); };

  var status = panel.querySelector('[data-el="status"]');
  function say(msg, err) {
    status.textContent = msg;
    status.style.color = err ? '#f87171' : '#94a3b8';
  }

  function sleep(ms) { return new Promise(function (r) { setTimeout(r, ms); }); }

  function zumReady() {
    return typeof window.z_um !== 'undefined' && typeof window.z_um.getToken === 'function';
  }

  // The z_um SDK appears only after the first message is sent in this tab.
  // Mirror the upstream collector: type "__" and click send.
  async function ensureZum() {
    if (zumReady()) return true;
    say('Triggering z_um SDK (sending "__")…');
    try {
      // wait for the chat input (page may still be loading)
      var input = null, btn = null;
      for (var w = 0; w < 30 && !input; w++) {
        input = document.querySelector('#chat-input');
        btn = document.querySelector('#send-message-button');
        if (!input || !btn) { input = null; await sleep(500); }
      }
      if (!input || !btn) throw new Error('chat input not found');
      input.focus();
      // React-controlled inputs need the native setter path
      var setter = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, 'value') ||
                   Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value');
      if (setter && setter.set) setter.set.call(input, '__');
      else input.value = '__';
      input.dispatchEvent(new Event('input', { bubbles: true }));
      await sleep(150);
      btn.click();
    } catch (e) {
      say('Could not auto-send — send any message yourself, then press Collect again.', true);
      return false;
    }
    for (var i = 0; i < 120; i++) {
      if (zumReady()) return true;
      await sleep(250);
    }
    say('z_um SDK did not appear — send a message in the chat, then press Collect again.', true);
    return false;
  }

  async function collect() {
    var n = Math.min(1000, Math.max(10, parseInt(panel.querySelector('[data-el="count"]').value, 10) || 250));
    var btn = panel.querySelector('[data-act="collect"]');
    btn.disabled = true;
    try {
      if (!await ensureZum()) return;
      var out = [], seen = Object.create(null), t0 = Date.now();
      for (var i = 0; i < n; i++) {
        var tok = window.z_um.getToken();
        if (tok && typeof tok.then === 'function') tok = await tok;
        if (tok && !seen[tok]) { seen[tok] = 1; out.push(tok); }
        if (i % 10 === 0) say('Collected ' + out.length + ' / ' + n + ' …');
        if (i % 50 === 0) await sleep(0); // keep the page responsive
      }
      if (!out.length) { say('No tokens collected — reload the page and try again.', true); return; }

      var text = out.join('\n');
      var stamp = new Date().toISOString().slice(0, 16).replace(/[:T-]/g, '');
      var name = 'zum-tokens-' + stamp + '.txt';

      // 1) download (goes to /Downloads — Termux reads it via 'gz2a refresh')
      try {
        var a = document.createElement('a');
        a.href = URL.createObjectURL(new Blob([text], { type: 'text/plain' }));
        a.download = name;
        document.body.appendChild(a); a.click(); a.remove();
      } catch (e) { /* clipboard below is the fallback */ }

      // 2) clipboard (works on Android Chrome from a user gesture)
      try { await navigator.clipboard.writeText(text); } catch (e) { /* download is the fallback */ }

      say('✔ ' + out.length + ' tokens in ' + ((Date.now() - t0) / 1000).toFixed(1) +
          's → ' + name + ' (Downloads) + clipboard', false);
    } catch (e) {
      say('Error: ' + e.message, true);
    } finally {
      btn.disabled = false;
    }
  }

  function copyZai() {
    var tok = null;
    try { tok = localStorage.getItem('token'); } catch (e) {}
    if (!tok) { say('Not logged in (guest). Log in to Z.AI first, then press again.', true); return; }
    navigator.clipboard.writeText(tok).then(
      function () {
        say('Z.AI token copied (' + tok.slice(0, 18) + '…)\nIn Termux run:\n  gz2a settoken <paste>',
            false);
      },
      function () {
        window.prompt('Copy your Z.AI token:', tok);
      }
    );
  }

  panel.querySelector('[data-act="collect"]').onclick = collect;
  panel.querySelector('[data-act="zai"]').onclick = copyZai;
})();
