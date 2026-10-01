// Upgrades the card-page infographic teaser from a plain link to the full image
// into an in-page viewer. Without JavaScript, or without <dialog> support, the
// link still opens the image itself.
(function () {
  var dialog = document.getElementById('ig-lightbox');
  if (!dialog || typeof dialog.showModal !== 'function') return;

  var img = dialog.querySelector('img[data-src]');
  var opener = null;

  document.querySelectorAll('[data-ig-open]').forEach(function (link) {
    link.setAttribute('aria-haspopup', 'dialog');
    link.addEventListener('click', function (e) {
      // Leave modified clicks alone so "open in new tab" still works.
      if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
      e.preventDefault();
      if (img && !img.getAttribute('src')) img.setAttribute('src', img.getAttribute('data-src'));
      opener = link;
      dialog.showModal();
      offerZoom();
    });
  });

  // On a screen narrower than the image, offer a zoom: the image at its real
  // width, panned inside the viewer, instead of type shrunk to a third.
  var frame = dialog.querySelector('.ig-lightbox-frame');
  var zoom = dialog.querySelector('[data-ig-zoom]');
  function setZoom(on) {
    if (!zoom || !frame) return;
    frame.classList.toggle('is-zoomed', on);
    zoom.setAttribute('aria-pressed', on ? 'true' : 'false');
    zoom.textContent = on ? 'Zoom out' : 'Zoom';
  }
  if (zoom && frame && img) {
    zoom.addEventListener('click', function () {
      setZoom(zoom.getAttribute('aria-pressed') !== 'true');
    });
    dialog.addEventListener('close', function () { setZoom(false); });
  }
  function offerZoom() {
    if (!zoom || !frame || !img) return;
    zoom.hidden = frame.clientWidth >= (parseInt(img.getAttribute('width'), 10) || 1024);
  }

  var close = dialog.querySelector('[data-ig-close]');
  if (close) close.addEventListener('click', function () { dialog.close(); });

  // A click on the backdrop lands on the dialog element itself; clicks inside
  // the frame land on its children.
  dialog.addEventListener('click', function (e) {
    if (e.target === dialog) dialog.close();
  });

  dialog.addEventListener('close', function () {
    if (opener) opener.focus();
  });
})();
