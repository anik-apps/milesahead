// Marks the section being read in the card page's table of contents and in
// the phone pills (aria-current), and keeps the current pill in view in its
// sideways-scrolling row. Without JavaScript the links still work; only the
// highlight is missing.
(function () {
  if (!('IntersectionObserver' in window)) return;
  var links = Array.prototype.slice.call(document.querySelectorAll('.card-toc a[href^="#"], .card-mobile-pills a[href^="#"]'));
  if (!links.length) return;
  var pills = document.querySelector('.card-mobile-pills');
  var reduce = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  var targets = [];
  links.forEach(function (a) {
    var el = document.getElementById(a.getAttribute('href').slice(1));
    if (el && targets.indexOf(el) < 0) targets.push(el);
  });

  var current = null;
  function mark(id) {
    if (id === current) return;
    current = id;
    links.forEach(function (a) {
      if (a.getAttribute('href') === '#' + id) {
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

  // The current section is the last one whose top has passed the upper third
  // of the screen.
  var visible = {};
  var observer = new IntersectionObserver(function (entries) {
    entries.forEach(function (e) { visible[e.target.id] = e.isIntersecting; });
    for (var i = 0; i < targets.length; i++) {
      if (visible[targets[i].id]) { mark(targets[i].id); return; }
    }
  }, { rootMargin: '-15% 0px -70% 0px' });
  targets.forEach(function (t) { observer.observe(t); });

  // The last sections may be too short to reach the upper third; at the very
  // bottom of the page, the last one is current.
  window.addEventListener('scroll', function () {
    if (window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 4) {
      mark(targets[targets.length - 1].id);
    }
  }, { passive: true });
})();
