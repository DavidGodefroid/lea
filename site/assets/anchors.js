// Section links: every titled section (h2/h3 with an id) gets a "#" link.
// Clicking the title or its "#" puts the section's URL in the address bar and
// copies it, so it can be sent to someone who lands on that paragraph.
(function () {
  "use strict";

  var status = document.createElement("div");
  status.className = "anchor-toast";
  status.setAttribute("role", "status");
  status.setAttribute("aria-live", "polite");
  document.body.appendChild(status);
  var hideTimer;

  function announce(text) {
    status.textContent = text;
    status.classList.add("is-visible");
    clearTimeout(hideTimer);
    hideTimer = setTimeout(function () { status.classList.remove("is-visible"); }, 1800);
  }

  function copy(url) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(url);
    }
    return new Promise(function (resolve, reject) {
      var field = document.createElement("textarea");
      field.value = url;
      field.setAttribute("readonly", "");
      field.style.position = "fixed";
      field.style.opacity = "0";
      document.body.appendChild(field);
      field.select();
      var ok = false;
      try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
      document.body.removeChild(field);
      ok ? resolve() : reject();
    });
  }

  function share(id) {
    var url = location.href.split("#")[0] + "#" + id;
    if (history.replaceState) history.replaceState(null, "", "#" + id);
    else location.hash = id;
    copy(url).then(
      function () { announce("Link copied"); },
      function () { announce("Link in the address bar"); }
    );
  }

  var headings = document.querySelectorAll("main h2[id], main h3[id]");
  Array.prototype.forEach.call(headings, function (h) {
    if (h.classList.contains("visually-hidden")) return;
    var id = h.id;
    var link = document.createElement("a");
    link.className = "heading-anchor";
    link.href = "#" + id;
    link.textContent = "#";
    link.setAttribute("aria-label", "Copy link to “" + h.textContent.trim() + "”");
    h.appendChild(link);
    h.classList.add("has-anchor");

    h.addEventListener("click", function (event) {
      // Leave other links in the title alone, and a text selection too.
      var target = event.target.closest("a");
      if (target && target !== link) return;
      if (target !== link && String(window.getSelection()).length > 0) return;
      event.preventDefault();
      share(id);
    });
  });
})();

// On a phone the navigation scrolls sideways: bring the current page's link
// into view, so a reader on FAQ or Security sees where they are.
(function () {
  "use strict";
  var current = document.querySelector(".site-nav [aria-current=\"page\"]");
  if (!current) return;
  var row = current.closest("ul");
  if (row.scrollWidth <= row.clientWidth) return;
  var offset = current.getBoundingClientRect().left - row.getBoundingClientRect().left;
  row.scrollLeft += offset - (row.clientWidth - current.offsetWidth) / 2;
})();
