// lea web's sign-in on the network (#416): the token goes in a POST body, never in an address.
'use strict';
document.getElementById('signin').addEventListener('submit', async (ev) => {
  ev.preventDefault();
  const err = document.getElementById('signin-error');
  err.hidden = true;
  const token = document.getElementById('token').value.trim();
  let res;
  try {
    res = await fetch('/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token }), credentials: 'same-origin' });
  } catch (e) {
    err.textContent = 'lea web does not answer.';
    err.hidden = false;
    return;
  }
  if (res.ok) { location.replace('/'); return; }
  const body = await res.json().catch(() => ({}));
  err.textContent = body.error || ('sign-in refused (' + res.status + ')');
  err.hidden = false;
});
