// Popup posts directly to the daemon. No auth: it's localhost, single-user.
const DAEMON = "http://localhost:7777";

const form = document.getElementById("capture-form");
const input = document.getElementById("text");
const err = document.getElementById("err");

form.addEventListener("submit", async (e) => {
  e.preventDefault();
  const text = input.value.trim();
  if (!text) return;
  err.hidden = true;
  try {
    const resp = await fetch(`${DAEMON}/api/capture`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text }),
    });
    if (!resp.ok) throw new Error(`daemon ${resp.status}`);
    window.close();
  } catch (e2) {
    err.hidden = false;
    err.textContent = String(e2.message || e2);
  }
});
