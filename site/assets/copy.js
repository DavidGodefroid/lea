// copy.js: a Copy button on every install box (.install), shared by the
// Lock sites. Progressive enhancement: without JavaScript the box is a plain
// <pre> whose commands can be selected by hand.
//
// It copies the commands only: the text of each .cmd line when the box has
// them, otherwise every line of the <pre> without a leading "$ " prompt,
// skipping blank lines and comment lines (#) and dropping .cmt comments.
// navigator.clipboard where the page is a secure context, a hidden textarea
// and execCommand("copy") otherwise; the result is announced in an
// aria-live region ("Copied" / "Press Ctrl+C to copy").
(function () {
  "use strict";

  function commands(pre) {
    var lines = pre.querySelectorAll(".cmd");
    if (lines.length) {
      return Array.prototype.map.call(lines, function (l) { return l.textContent.trim(); }).join("\n");
    }
    var clone = pre.cloneNode(true);
    Array.prototype.forEach.call(clone.querySelectorAll(".cmt"), function (c) { c.remove(); });
    return clone.textContent.split("\n")
      .map(function (l) { return l.replace(/^\s*\$\s+/, "").replace(/\s+$/, ""); })
      .filter(function (l) { return l && l.charAt(0) !== "#"; })
      .join("\n");
  }

  function fallbackCopy(text) {
    var field = document.createElement("textarea");
    field.value = text;
    field.setAttribute("readonly", "");
    field.style.position = "fixed";
    field.style.top = "0";
    field.style.opacity = "0";
    document.body.appendChild(field);
    field.select();
    var ok = false;
    try { ok = document.execCommand("copy"); } catch (e) { ok = false; }
    document.body.removeChild(field);
    return ok ? Promise.resolve() : Promise.reject(new Error("copy refused"));
  }

  function copy(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text).catch(function () { return fallbackCopy(text); });
    }
    return fallbackCopy(text);
  }

  function selectPre(pre) {
    var range = document.createRange();
    range.selectNodeContents(pre);
    var sel = window.getSelection();
    sel.removeAllRanges();
    sel.addRange(range);
  }

  Array.prototype.forEach.call(document.querySelectorAll(".install"), function (box) {
    var pre = box.querySelector("pre");
    if (!pre) return;
    var head = box.querySelector(".install-head");
    if (!head) {
      head = document.createElement("div");
      head.className = "install-head";
      box.insertBefore(head, box.firstChild);
    }
    var button = document.createElement("button");
    button.type = "button";
    button.className = "copy";
    button.textContent = "Copy";
    var label = (box.getAttribute("data-copy-label") || "Copy the install commands");
    button.setAttribute("aria-label", label);
    var status = document.createElement("span");
    status.className = "visually-hidden";
    status.setAttribute("role", "status");
    status.setAttribute("aria-live", "polite");
    head.appendChild(button);
    head.appendChild(status);

    var timer;
    function show(state, text, said) {
      button.setAttribute("data-state", state);
      button.textContent = text;
      status.textContent = said;
      clearTimeout(timer);
      timer = setTimeout(function () {
        button.removeAttribute("data-state");
        button.textContent = "Copy";
        status.textContent = "";
      }, 2000);
    }

    button.addEventListener("click", function () {
      copy(commands(pre)).then(
        function () { show("done", "Copied", "Copied"); },
        function () { selectPre(pre); show("fail", "Press Ctrl+C", "Selected: press Ctrl+C to copy"); }
      );
    });
  });
})();
