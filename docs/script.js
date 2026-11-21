/* RateGuard landing page: nav, copy buttons, and the hero ledger replay.
   Figures match the limiter exactly. X-RateLimit-Limit is the bucket capacity
   (burst), and Retry-After is 60 / limit_rpm seconds. This ledger models
   limit_rpm = 5, burst = 60, so one token frees up every 12 seconds — which is
   why the shed row reads Retry-After: 12. */
(function () {
  "use strict";

  var toggle = document.querySelector(".nav-toggle");
  var nav = document.getElementById("primary-nav");

  function setOpen(open) {
    if (!toggle || !nav) return;
    nav.classList.toggle("is-open", open);
    toggle.setAttribute("aria-expanded", open ? "true" : "false");
    toggle.querySelector(".nav-toggle-label").textContent = open ? "Close" : "Menu";
  }

  if (toggle && nav) {
    toggle.addEventListener("click", function () {
      setOpen(toggle.getAttribute("aria-expanded") !== "true");
    });

    nav.addEventListener("click", function (event) {
      if (event.target.closest("a")) setOpen(false);
    });

    document.addEventListener("keydown", function (event) {
      if (event.key === "Escape") setOpen(false);
    });

    document.addEventListener("click", function (event) {
      if (!nav.contains(event.target) && !toggle.contains(event.target)) setOpen(false);
    });
  }

  document.querySelectorAll("[data-copy]").forEach(function (button) {
    button.addEventListener("click", function () {
      var target = document.getElementById(button.getAttribute("data-copy"));
      if (!target || !navigator.clipboard) return;
      navigator.clipboard.writeText(target.textContent || "").then(function () {
        var original = button.textContent;
        button.textContent = "Copied";
        window.setTimeout(function () { button.textContent = original; }, 1600);
      }).catch(function () { /* clipboard is unavailable on file:// */ });
    });
  });

  var year = document.getElementById("year");
  if (year) year.textContent = String(new Date().getFullYear());

  var ledger = document.getElementById("demo-ledger");
  var remaining = document.getElementById("demo-remaining");
  var limit = document.getElementById("demo-limit");
  var fill = document.getElementById("demo-fill");
  var shed = document.getElementById("demo-shed");
  var replay = document.getElementById("demo-replay");
  var panel = replay ? replay.closest(".ledger") : null;

  if (!ledger || !remaining || !fill || !shed || !replay) return;

  var CAPACITY = 60;
  var MS_PER_REQUEST = 320;
  var timers = [];

  var reduceMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  function clearTimers() {
    timers.forEach(window.clearTimeout);
    timers = [];
  }

  function paint(tokens) {
    var whole = Math.max(0, Math.min(CAPACITY, Math.round(tokens)));
    var changed = remaining.textContent !== String(whole);
    remaining.textContent = String(whole);
    if (limit) limit.textContent = String(CAPACITY);
    fill.style.width = (whole / CAPACITY) * 100 + "%";
    fill.classList.toggle("is-low", whole <= 12);
    remaining.classList.toggle("is-critical", whole <= 12);

    if (changed && !reduceMotion) {
      remaining.classList.remove("is-tick");
      // Force a reflow so the animation restarts on every decrement.
      void remaining.offsetWidth;
      remaining.classList.add("is-tick");
    }
    return whole;
  }

  function addRow(tokens, latency) {
    var row = document.createElement("li");
    row.className = "ledger-row" + (reduceMotion ? "" : " is-entering");

    var code = document.createElement("span");
    code.className = "row-code is-ok num";
    code.textContent = "200";

    var key = document.createElement("code");
    key.className = "row-key";
    key.textContent = "ag_live_9f2c8b1d…";

    var pair = document.createElement("span");
    pair.className = "row-remaining num";
    pair.textContent = tokens + " / " + CAPACITY;

    var ms = document.createElement("span");
    ms.className = "row-ms num";
    ms.textContent = latency + " ms";

    row.append(code, key, pair, ms);
    ledger.prepend(row);

    while (ledger.children.length > 4) {
      ledger.removeChild(ledger.lastChild);
    }
  }

  function showShed() {
    shed.hidden = false;
    if (!reduceMotion) shed.classList.add("is-entering");
    if (panel) {
      panel.classList.remove("is-running");
      panel.classList.add("is-shed-active");
    }
    replay.textContent = "Replay again";
  }

  function run() {
    clearTimers();
    shed.hidden = true;
    shed.classList.remove("is-entering");
    ledger.textContent = "";
    replay.disabled = true;
    replay.textContent = "Running…";
    if (panel) {
      panel.classList.remove("is-shed-active");
      panel.classList.add("is-running");
    }

    var tokens = CAPACITY;
    paint(tokens);

    var settle = reduceMotion ? 0 : 420;

    for (var i = 1; i <= 8; i += 1) {
      (function (step) {
        timers.push(window.setTimeout(function () {
          tokens = Math.max(0, tokens - 1);
          paint(tokens);
          addRow(tokens, 14 + step * 2);
          if (step === 8) {
            timers.push(window.setTimeout(function () {
              showShed();
              replay.disabled = false;
            }, settle));
          }
        }, reduceMotion ? 0 : step * MS_PER_REQUEST));
      })(i);
    }
  }

  replay.addEventListener("click", run);
})();