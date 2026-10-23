// The page: lists the notes and adds one. Text goes in as text
// (textContent), never as HTML.
const list = document.getElementById('notes');
const error = document.getElementById('error');

function show(notes) {
  list.replaceChildren(...notes.map((n) => {
    const li = document.createElement('li');
    const by = document.createElement('small');
    by.textContent = ` — ${n.by}, ${new Date(n.at).toLocaleString()}`;
    li.append(n.text, by);
    return li;
  }));
}

async function call(init) {
  const r = await fetch('api/notes', { credentials: 'same-origin', ...init });
  const body = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(body.error || `Error ${r.status}`);
  return body.notes;
}

document.getElementById('add').addEventListener('submit', async (e) => {
  e.preventDefault();
  const input = document.getElementById('text');
  error.textContent = '';
  try {
    show(await call({ method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ text: input.value }) }));
    input.value = '';
  } catch (err) {
    error.textContent = err.message;
  }
});

call().then(show, (err) => { error.textContent = err.message; });
