(function () {
  var form = document.getElementById('autoform');
  if (form) setTimeout(function () { form.submit(); }, 50);

  var app = document.getElementById('openapp');
  if (app) setTimeout(function () { window.location.href = app.getAttribute('href'); }, 300);

  var methods = document.getElementById('methods');
  if (methods) methods.addEventListener('submit', function () {
    setTimeout(function () {
      methods.querySelectorAll('button').forEach(function (b) { b.disabled = true; });
    }, 0);
  });

  var poll = document.getElementById('poll');
  if (!poll) return;
  var statusURL = poll.getAttribute('data-status'), doneURL = poll.getAttribute('data-done');
  var n = 0;
  function tick() {
    if (++n > 400) return; // ~20 minutes
    fetch(statusURL, { cache: 'no-store' }).then(function (r) { return r.json(); }).then(function (d) {
      if (d.status === 'PAID' || d.status === 'CLOSED') { window.location.href = doneURL; return; }
      setTimeout(tick, 3000);
    }).catch(function () { setTimeout(tick, 5000); });
  }
  setTimeout(tick, 3000);
  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'visible') { n = 0; }
  });
})();
