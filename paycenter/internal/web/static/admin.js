(function () {
  'use strict';
  var $ = function (s, el) { return (el || document).querySelector(s); };
  var $$ = function (s, el) { return Array.prototype.slice.call((el || document).querySelectorAll(s)); };
  var CHECK = '<svg class="ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg>';

  // ---- toasts ----
  function hideLater(t, ms) {
    setTimeout(function () { t.classList.add('hide'); setTimeout(function () { t.remove(); }, 320); }, ms);
  }
  function toast(msg) {
    var box = $('.toasts');
    if (!box) { box = document.createElement('div'); box.className = 'toasts'; document.body.appendChild(box); }
    var t = document.createElement('div');
    t.className = 'toast';
    t.innerHTML = CHECK + '<div></div>';
    t.lastChild.textContent = msg;
    box.appendChild(t);
    hideLater(t, 2200);
  }
  $$('.toast').forEach(function (t) { hideLater(t, 5000); });
  // drop ?ok= / ?err= so a reload doesn't show the message again
  if (/[?&](ok|err)=/.test(location.search) && history.replaceState) {
    var u = new URL(location.href); u.searchParams.delete('ok'); u.searchParams.delete('err');
    history.replaceState(null, '', u.pathname + u.search + u.hash);
  }
  document.addEventListener('click', function (e) {
    var x = e.target.closest('[data-dismiss]');
    if (x) { var a = x.closest('.alert,.toast'); if (a) a.remove(); }
  });

  // ---- user menu / mobile sidebar ----
  var ub = $('#userBtn'), um = $('#userMenu');
  if (ub) {
    ub.addEventListener('click', function (e) { e.stopPropagation(); um.classList.toggle('open'); });
    document.addEventListener('click', function () { um.classList.remove('open'); });
  }
  var mb = $('#menuBtn'), side = $('.side'), scrim = $('.scrim');
  if (mb) {
    mb.addEventListener('click', function () { side.classList.add('open'); scrim.classList.add('on'); });
    scrim.addEventListener('click', function () { side.classList.remove('open'); scrim.classList.remove('on'); });
  }

  // ---- copy ----
  function copyText(t) {
    if (navigator.clipboard && window.isSecureContext) return navigator.clipboard.writeText(t);
    return new Promise(function (res) {
      var a = document.createElement('textarea');
      a.value = t; a.style.position = 'fixed'; a.style.opacity = '0';
      document.body.appendChild(a); a.select();
      try { document.execCommand('copy'); } catch (_) { }
      a.remove(); res();
    });
  }
  document.addEventListener('click', function (e) {
    var b = e.target.closest('[data-copy],[data-copy-from]');
    if (!b) return;
    e.preventDefault();
    var v = b.getAttribute('data-copy');
    if (v === null) {
      var src = document.getElementById(b.getAttribute('data-copy-from'));
      v = src ? (src.value !== undefined && src.tagName !== 'CODE' ? src.value : src.textContent) : '';
    }
    copyText(String(v).trim()).then(function () { toast('已复制到剪贴板'); });
  });

  // ---- reveal secret ----
  document.addEventListener('click', function (e) {
    var b = e.target.closest('[data-reveal]');
    if (!b) return;
    var i = document.getElementById(b.getAttribute('data-reveal'));
    i.type = i.type === 'password' ? 'text' : 'password';
    b.title = i.type === 'password' ? '显示' : '隐藏';
  });

  // ---- busy buttons on submit ----
  function busy(form, submitter) {
    var btn = submitter || form.querySelector('[type=submit]');
    if (!btn) return;
    setTimeout(function () { // after the browser has read the submitter's name/value
      btn.disabled = true;
      btn.setAttribute('data-html', btn.innerHTML);
      btn.innerHTML = '<span class="spin"></span>' + (btn.getAttribute('data-busy') || '处理中…');
    }, 0);
  }
  window.addEventListener('pageshow', function () {
    $$('[data-html]').forEach(function (b) { b.disabled = false; b.innerHTML = b.getAttribute('data-html'); b.removeAttribute('data-html'); });
  });

  // ---- confirm dialog ----
  var dlg = $('#confirmDlg'), pending = null;
  $$('form').forEach(function (f) {
    if ((f.getAttribute('method') || '').toLowerCase() !== 'post') return;
    f.addEventListener('submit', function (e) {
      if (f.hasAttribute('data-confirm') && !f.getAttribute('data-ok') && dlg && dlg.showModal) {
        e.preventDefault();
        $('#confirmTitle').textContent = f.getAttribute('data-title') || '请确认';
        $('#confirmMsg').textContent = f.getAttribute('data-confirm');
        var yes = $('#confirmYes');
        yes.className = 'btn ' + (f.hasAttribute('data-danger') ? 'btn-danger-solid' : 'btn-pri');
        yes.textContent = f.getAttribute('data-yes') || '确定';
        pending = f;
        dlg.showModal();
        return;
      }
      busy(f, e.submitter);
    });
  });
  if (dlg) {
    $('#confirmYes').addEventListener('click', function () {
      dlg.close();
      if (pending) { pending.setAttribute('data-ok', '1'); busy(pending); pending.submit(); }
    });
    $('#confirmNo').addEventListener('click', function () { dlg.close(); pending = null; });
  }

  // ---- merchant: generate keys ----
  var gen = $('#genkeys');
  if (gen) gen.addEventListener('click', function () {
    var f = $('#mform');
    var go = function () {
      var body = new URLSearchParams({ _csrf: gen.getAttribute('data-csrf') });
      fetch('/admin/keys', { method: 'POST', body: body, credentials: 'same-origin' })
        .then(function (r) { return r.json(); })
        .then(function (k) {
          f.merchant_id.value = k.merchant_id; f.sign_key.value = k.sign_key; f.enc_key.value = k.enc_key;
          f.sign_key.type = 'text'; f.enc_key.type = 'text';
          toast('已生成新的商户号和密钥，保存后生效');
        });
    };
    if (f.merchant_id.value && dlg && dlg.showModal) {
      $('#confirmTitle').textContent = '覆盖商户号和密钥';
      $('#confirmMsg').textContent = '保存后旧密钥立即失效，VPN 后台也要同步修改，否则验签会失败。';
      $('#confirmYes').className = 'btn btn-danger-solid'; $('#confirmYes').textContent = '覆盖';
      pending = null; dlg.showModal();
      var once = function () { $('#confirmYes').removeEventListener('click', once); go(); };
      $('#confirmYes').addEventListener('click', once);
    } else { go(); }
  });

  // ---- channel: sign type toggle, pay type rows ----
  var rsa = $('#rsaBlock');
  if (rsa) {
    var sync = function () { var v = $('input[name=sign_type]:checked'); rsa.style.display = v && v.value === 'RSA' ? '' : 'none'; };
    $$('input[name=sign_type]').forEach(function (i) { i.addEventListener('change', sync); });
    sync();
  }
  var ptList = $('#ptList');
  if (ptList) {
    $('#ptAdd').addEventListener('click', function () {
      var row = $('#ptTpl').content.firstElementChild.cloneNode(true);
      ptList.appendChild(row); row.querySelector('input').focus();
    });
    ptList.addEventListener('click', function (e) {
      var d = e.target.closest('[data-pt-del]');
      if (!d) return;
      var rows = $$('.pt-row', ptList);
      if (rows.length > 1) d.closest('.pt-row').remove();
      else $$('input', rows[0]).forEach(function (i) { i.value = ''; });
    });
  }

  // ---- test detail: reload when the order changes (payment, notification) ----
  var st = $('#runState');
  if (st) {
    var url = st.getAttribute('data-url'), cur = st.getAttribute('data-state'), n = 0;
    var tick = function () {
      if (++n > 300) return;
      if (document.visibilityState !== 'visible') { setTimeout(tick, 4000); return; }
      fetch(url, { credentials: 'same-origin', cache: 'no-store' }).then(function (r) { return r.json(); }).then(function (d) {
        if (d.state && d.state !== cur) { location.reload(); return; }
        setTimeout(tick, 4000);
      }).catch(function () { setTimeout(tick, 8000); });
    };
    setTimeout(tick, 4000);
  }
})();
