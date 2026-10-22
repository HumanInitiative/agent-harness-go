// Draft donation report page. All text from the server is shown as plain
// text (textContent), never parsed as HTML.
(function () {
  "use strict";

  var $ = function (id) { return document.getElementById(id); };
  var keyInput = $("key"), programSelect = $("program"), fromInput = $("from"), toInput = $("to");
  var statusEl = $("status"), result = $("result");
  var buttons = [$("load"), $("preview"), $("download")];

  var ERRORS = {
    unauthorized: "Kunci API tidak valid.",
    rate_limited: "Terlalu banyak permintaan. Tunggu sebentar lalu coba lagi.",
    program_not_found: "Program tidak ditemukan di data donasi.",
    no_documents: "Program ini belum punya dokumen kegiatan yang bisa dibaca, jadi narasi belum bisa dibuat.",
    narrative_failed: "AI belum menghasilkan narasi yang bisa dipakai. Coba lagi.",
    model_unavailable: "Layanan AI sedang sibuk. Coba lagi sebentar lagi.",
    timeout: "Penyusunan draf terlalu lama. Coba lagi.",
    request_too_large: "Permintaan terlalu besar."
  };

  function setStatus(text, isError) {
    statusEl.textContent = text;
    statusEl.className = isError ? "error" : "";
  }

  function setBusy(busy) {
    buttons.forEach(function (b) { b.disabled = busy; });
  }

  function readKey() {
    var key = keyInput.value.trim();
    try { sessionStorage.setItem("reportKey", key); } catch (e) { /* private mode: fine */ }
    return key;
  }

  // call sends a request with the API key and resolves with the Response, or
  // rejects with an Error whose message is meant for the user.
  function call(path, options) {
    var key = readKey();
    if (!key) { return Promise.reject(new Error("Isi kunci API dulu.")); }
    options = options || {};
    options.headers = Object.assign({ "X-API-Key": key }, options.headers || {});
    return fetch(path, options).then(function (res) {
      if (res.ok) { return res; }
      return res.json().catch(function () { return {}; }).then(function (body) {
        var msg = ERRORS[body.code] || body.error || ("Terjadi kesalahan (HTTP " + res.status + ").");
        throw new Error(msg);
      });
    }, function () {
      throw new Error("Tidak bisa menghubungi server.");
    });
  }

  function loadPrograms() {
    setBusy(true);
    setStatus("Memuat program…");
    return call("/api/v1/programs").then(function (res) { return res.json(); }).then(function (programs) {
      programSelect.textContent = "";
      if (!programs.length) {
        programSelect.add(new Option("Belum ada program", ""));
        programSelect.disabled = true;
        setStatus("Belum ada program di data donasi.", true);
        return;
      }
      programSelect.add(new Option("Pilih program", ""));
      programs.forEach(function (p) { programSelect.add(new Option(p.name, p.id)); });
      programSelect.disabled = false;
      setStatus("");
    }).catch(function (err) {
      setStatus(err.message, true);
    }).then(function () { setBusy(false); });
  }

  // request validates the form and returns the report request, or null after
  // telling the user what is missing.
  function request(format) {
    if (programSelect.disabled) { setStatus("Isi kunci API lalu tekan \"Muat program\" dulu.", true); return null; }
    if (!programSelect.value) { setStatus("Pilih program dulu.", true); return null; }
    if (!fromInput.value || !toInput.value) { setStatus("Isi tanggal mulai dan akhir.", true); return null; }
    if (toInput.value < fromInput.value) { setStatus("Tanggal akhir tidak boleh sebelum tanggal mulai.", true); return null; }
    var body = { program_id: programSelect.value, from: fromInput.value, to: toInput.value };
    if (format) { body.format = format; }
    return { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
  }

  function fill(list, items) {
    list.textContent = "";
    items.forEach(function (text) {
      var li = document.createElement("li");
      li.textContent = text;
      list.appendChild(li);
    });
  }

  function preview(event) {
    event.preventDefault();
    var req = request();
    if (!req) { return; }
    setBusy(true);
    result.hidden = true;
    setStatus("Menyusun draf… ini bisa memakan waktu sampai satu menit.");
    call("/api/v1/reports", req).then(function (res) { return res.json(); }).then(function (report) {
      $("draft").textContent = report.markdown;
      fill($("notes"), report.notes || []);
      $("notes-box").hidden = !(report.notes && report.notes.length);
      fill($("sources"), report.sources || []);
      result.hidden = false;
      setStatus(report.notes && report.notes.length
        ? "Draf siap, dengan catatan yang perlu diperiksa."
        : "Draf siap. Tetap perlu direview sebelum dikirim.");
    }).catch(function (err) {
      setStatus(err.message, true);
    }).then(function () { setBusy(false); });
  }

  function download() {
    var req = request("docx");
    if (!req) { return; }
    setBusy(true);
    setStatus("Menyusun file Word… ini bisa memakan waktu sampai satu menit.");
    var filename = "laporan.docx";
    call("/api/v1/reports", req).then(function (res) {
      var disposition = res.headers.get("Content-Disposition") || "";
      var match = /filename="([^"]+)"/.exec(disposition);
      if (match) { filename = match[1]; }
      return res.blob();
    }).then(function (blob) {
      var url = URL.createObjectURL(blob);
      var a = document.createElement("a");
      a.href = url;
      a.download = filename;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(function () { URL.revokeObjectURL(url); }, 10000);
      setStatus("File Word diunduh. Baca bagian \"Periksa sebelum dikirim\" di dalamnya.");
    }).catch(function (err) {
      setStatus(err.message, true);
    }).then(function () { setBusy(false); });
  }

  // Default period: the previous calendar quarter.
  function defaultPeriod() {
    var now = new Date();
    var q = Math.floor(now.getMonth() / 3);
    var start = new Date(now.getFullYear(), (q - 1) * 3, 1);
    var end = new Date(now.getFullYear(), q * 3, 0);
    var iso = function (d) {
      return d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, "0") + "-" + String(d.getDate()).padStart(2, "0");
    };
    fromInput.value = iso(start);
    toInput.value = iso(end);
  }

  $("form").addEventListener("submit", preview);
  $("download").addEventListener("click", download);
  $("load").addEventListener("click", loadPrograms);
  keyInput.addEventListener("keydown", function (e) {
    if (e.key === "Enter") { e.preventDefault(); loadPrograms(); }
  });

  defaultPeriod();
  try {
    var saved = sessionStorage.getItem("reportKey");
    if (saved) { keyInput.value = saved; loadPrograms(); }
  } catch (e) { /* no storage: the user types the key */ }
})();
