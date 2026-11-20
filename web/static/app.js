/* RateGuard dashboard behaviour. No inline handlers, no eval, no key logging. */
(function () {
  "use strict";

  var STORAGE_KEY = "rg_demo_key";
  var FLOOD_TOTAL = 200;
  var FLOOD_CONCURRENCY = 8;
  var REQUEST_TIMEOUT_MS = 5000;

  function toast(message, state) {
    var el = document.getElementById("toast");
    if (!el) { return; }
    el.textContent = String(message).slice(0, 300);
    el.setAttribute("data-state", state || "info");
    el.classList.add("is-visible");
    window.clearTimeout(toast.timer);
    toast.timer = window.setTimeout(function () {
      el.classList.remove("is-visible");
    }, 4200);
  }

  function setStatus(text, state) {
    var el = document.getElementById("demo-status");
    if (!el) { return; }
    el.textContent = text;
    el.setAttribute("data-state", state || "idle");
  }

  function putText(id, value) {
    var el = document.getElementById(id);
    if (el) { el.textContent = value; }
  }

  function readKey() {
    var input = document.getElementById("demo-key");
    return input ? input.value.trim() : "";
  }

  function requestWithTimeout(url, options) {
    var controller = new AbortController();
    var timer = window.setTimeout(function () { controller.abort(); }, REQUEST_TIMEOUT_MS);
    var opts = Object.assign({}, options || {}, { signal: controller.signal });
    return fetch(url, opts).then(function (response) {
      window.clearTimeout(timer);
      return response;
    }, function (error) {
      window.clearTimeout(timer);
      throw error;
    });
  }

  function authHeaders() {
    var key = readKey();
    if (!key) { return null; }
    return { "Authorization": "Bearer " + key };
  }

  function requireKey() {
    var key = readKey();
    if (!key) {
      toast("Paste a tenant API key first.", "error");
      setStatus("Key required", "error");
      return false;
    }
    return true;
  }

  function showQuote(response) {
    var target = document.getElementById("demo-quote");
    if (!target) { return; }
    var remaining = response.headers.get("X-RateLimit-Remaining");
    var limit = response.headers.get("X-RateLimit-Limit");
    var retry = response.headers.get("Retry-After");

    if (!response.ok) {
      var detail = "";
      try {
        var body = response.json();
        detail = body && body.message ? body.message : "HTTP " + response.status;
      } catch (_) {
        detail = "HTTP " + response.status;
      }
      target.textContent = detail + (retry ? "\nRetry-After: " + retry + "s" : "");
      setStatus("HTTP " + response.status, "error");
    } else {
      response.json().then(function (data) {
        target.textContent = (data.quote || "No quote returned") +
          "\n— " + (data.author || "unknown") +
          (remaining !== null ? "\nRemaining: " + remaining + " / " + (limit || "?") : "");
        setStatus("Allowed 200", "ok");
      }).catch(function () {
        target.textContent = "Request allowed, but the response was not JSON.";
        setStatus("Allowed 200", "ok");
      });
    }
    if (remaining !== null) { putText("demo-remaining", remaining); }
  }

  function sendOne() {
    if (!requireKey()) { return; }
    var headers = authHeaders();
    setStatus("Sending…", "idle");
    requestWithTimeout("/proxy/quote", { headers: headers, cache: "no-store" }).then(function (response) {
      showQuote(response);
    }).catch(function () {
      setStatus("Request failed", "error");
      toast("The proxy request failed or timed out.", "error");
    });
  }

  function flood() {
    if (!requireKey()) { return; }
    var sendButton = document.getElementById("demo-send");
    var floodButton = document.getElementById("demo-flood");
    if (floodButton && floodButton.disabled) { return; }
    if (sendButton) { sendButton.disabled = true; }
    if (floodButton) { floodButton.disabled = true; }

    var headers = authHeaders();
    var counts = { ok: 0, limited: 0, other: 0 };
    var lastRetry = "";
    var next = 0;
    var started = Date.now();

    function worker() {
      if (next >= FLOOD_TOTAL) { return Promise.resolve(); }
      next += 1;
      return requestWithTimeout("/proxy/quote", { headers: headers, cache: "no-store" })
        .then(function (response) {
          if (response.status === 200) { counts.ok += 1; }
          else if (response.status === 429) {
            counts.limited += 1;
            lastRetry = response.headers.get("Retry-After") || lastRetry;
          } else { counts.other += 1; }
        })
        .catch(function () { counts.other += 1; })
        .then(worker);
    }

    var workers = [];
    for (var i = 0; i < FLOOD_CONCURRENCY; i += 1) { workers.push(worker()); }
    Promise.all(workers).then(function () {
      var elapsed = Date.now() - started;
      setStatus(counts.limited > 0 ? "429s observed" : "Flood complete", counts.limited > 0 ? "error" : "ok");
      putText("demo-allowed", String(counts.ok));
      putText("demo-blocked", String(counts.limited));
      putText("demo-remaining", counts.other > 0 ? String(counts.other) : "0");
      var quote = document.getElementById("demo-quote");
      if (quote) {
        quote.textContent = counts.ok + " allowed · " + counts.limited + " blocked (429) · " +
          counts.other + " other in " + elapsed + "ms" +
          (lastRetry ? "\nLast Retry-After: " + lastRetry + "s" : "");
      }
      refreshUsage();
    }).finally(function () {
      if (sendButton) { sendButton.disabled = false; }
      if (floodButton) { floodButton.disabled = false; }
    });
  }

  function refreshUsage() {
    var demo = document.getElementById("demo");
    if (!demo) { return Promise.resolve(); }
    var tenantID = demo.getAttribute("data-demo-tenant-id");
    if (!tenantID) { return Promise.resolve(); }
    return requestWithTimeout("/dashboard/api/usage/" + encodeURIComponent(tenantID), { cache: "no-store" })
      .then(function (response) {
        if (!response.ok) { return null; }
        return response.json();
      })
      .then(function (data) {
        if (!data) { return; }
        putText("demo-allowed", String(data.allowed));
        putText("demo-blocked", String(data.blocked));
        putText("demo-p95", String(data.p95_ms));
      })
      .catch(function () { /* usage polling is best-effort */ });
  }

  function initDemo() {
    var input = document.getElementById("demo-key");
    if (input) {
      try {
        var saved = window.sessionStorage.getItem(STORAGE_KEY);
        if (saved) { input.value = saved; }
        input.addEventListener("input", function () {
          try { window.sessionStorage.setItem(STORAGE_KEY, input.value.trim()); } catch (_) { /* storage may be blocked */ }
        });
      } catch (_) { /* storage may be blocked */ }
    }
    var send = document.getElementById("demo-send");
    var flood = document.getElementById("demo-flood");
    if (send) { send.addEventListener("click", sendOne); }
    if (flood) { flood.addEventListener("click", flood); }
    if (document.getElementById("demo")) {
      refreshUsage();
      window.setInterval(refreshUsage, 3000);
    }
  }

  function initCopyButtons() {
    document.querySelectorAll("[data-copy-target]").forEach(function (button) {
      button.addEventListener("click", function () {
        var target = document.getElementById(button.getAttribute("data-copy-target"));
        if (!target) { return; }
        var text = target.textContent || "";
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(text).then(function () {
            var original = button.textContent;
            button.textContent = "Copied";
            window.setTimeout(function () { button.textContent = original; }, 1800);
            toast("Key copied to clipboard.", "success");
          }).catch(function () { toast("Copy failed; select the key manually.", "error"); });
        } else {
          toast("Clipboard unavailable; select the key manually.", "error");
        }
      });
    });
  }

  function initClock() {
    var clock = document.querySelector("[data-clock]");
    if (!clock) { return; }
    function tick() { clock.textContent = new Date().toISOString().slice(11, 19) + " UTC"; }
    tick();
    window.setInterval(tick, 1000);
  }

  document.addEventListener("DOMContentLoaded", function () {
    initDemo();
    initCopyButtons();
    initClock();
    document.body.addEventListener("htmx:responseError", function (event) {
      var status = event.detail && event.detail.xhr ? event.detail.xhr.status : "unknown";
      toast("Request failed (HTTP " + status + ").", "error");
    });
    document.body.addEventListener("htmx:sendError", function () {
      toast("Request could not be sent.", "error");
    });
    document.body.addEventListener("rateguard:toast", function (event) {
      var detail = event.detail || {};
      toast(detail.message || "", detail.state);
    });
  });
})();
