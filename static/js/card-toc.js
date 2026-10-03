// Marks the section being read in the card page's table of contents and in
// the phone pills (aria-current), and keeps the current pill in view in its
// sideways-scrolling row. Without JavaScript the links still work; only the
// highlight is missing.
(function () {
  var links = Array.prototype.slice.call(document.querySelectorAll('.card-toc a[href^="#"], .card-mobile-pills a[href^="#"]'));
  if (!links.length) return;
  var pills = document.querySelector('.card-mobile-pills');
  var reduce = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  var targets = [];
  links.forEach(function (a) {
    var el = document.getElementById(a.getAttribute('href').slice(1));
    if (el && targets.indexOf(el) < 0) targets.push(el);
  });
  if (!targets.length) return;

  var current = null;
  function mark(id) {
    if (id === current) return;
    current = id;
    links.forEach(function (a) {
      if (id && a.getAttribute('href') === '#' + id) {
        a.setAttribute('aria-current', 'location');
        if (pills && pills.contains(a) && pills.offsetParent !== null) {
          var left = a.offsetLeft - pills.offsetLeft;
          if (left < pills.scrollLeft || left + a.offsetWidth > pills.scrollLeft + pills.clientWidth) {
            pills.scrollTo({ left: Math.max(0, left - 16), behavior: reduce ? 'auto' : 'smooth' });
          }
        }
      } else {
        a.removeAttribute('aria-current');
      }
    });
  }

  // The current section is the last one whose top has passed a line a third
  // of the way down the screen; at the very bottom of the page, the last one
  // (it may be too short to reach that line). Worked out afresh on every
  // scroll, so scrolling back up always moves the mark back.
  function update() {
    var doc = document.documentElement;
    if (window.innerHeight + window.scrollY >= doc.scrollHeight - 4) {
      mark(targets[targets.length - 1].id);
      return;
    }
    var line = window.innerHeight / 3;
    var id = null;
    for (var i = 0; i < targets.length; i++) {
      if (targets[i].getBoundingClientRect().top <= line) id = targets[i].id;
    }
    mark(id);
  }

  var queued = false;
  function onScroll() {
    if (queued) return;
    queued = true;
    window.requestAnimationFrame(function () { queued = false; update(); });
  }
  window.addEventListener('scroll', onScroll, { passive: true });
  window.addEventListener('resize', onScroll);
  update();
})();
