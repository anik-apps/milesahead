// Interactive architecture diagram (layouts/_shortcodes/architecture.html).
//
// The page works without this script: nodes link to their entries in the
// list under the diagram, and every entry is open. This turns the nodes into
// buttons that open a details panel (a copy of the node's list entry),
// highlights the node's edges, and collapses the list.
(function () {
  'use strict';

  var reduceMotion = window.matchMedia &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  function init(fig) {
    var svg = fig.querySelector('.arch-svg');
    var panel = fig.querySelector('.arch-panel');
    var title = panel.querySelector('.arch-panel-title');
    var kind = panel.querySelector('.arch-panel-kind');
    var body = panel.querySelector('.arch-panel-body');
    var closeBtn = panel.querySelector('.arch-panel-close');
    var status = fig.querySelector('.arch-status');
    var edges = Array.prototype.slice.call(svg.querySelectorAll('.arch-edge'));
    var nodes = Array.prototype.slice.call(svg.querySelectorAll('.arch-node'));
    var list = document.querySelector('.arch-list');
    var selected = null;

    fig.classList.add('arch--js');

    // The list starts open for readers without the script; collapse it.
    if (list) {
      Array.prototype.forEach.call(list.querySelectorAll('details.arch-item'), function (d) {
        d.open = false;
      });
    }

    nodes.forEach(function (node) {
      node.setAttribute('role', 'button');
      node.setAttribute('aria-controls', panel.id);
      node.setAttribute('aria-expanded', 'false');
      node.addEventListener('click', function (e) {
        e.preventDefault();
        toggle(node, e.detail === 0);
      });
      node.addEventListener('keydown', function (e) {
        // Enter already clicks a link; Space should press a button.
        if (e.key === ' ' || e.key === 'Spacebar') {
          e.preventDefault();
          toggle(node, true);
        }
      });
      node.addEventListener('mouseenter', function () { hover(node.dataset.id); });
      node.addEventListener('mouseleave', function () { hover(null); });
      node.addEventListener('focus', function () { hover(node.dataset.id); });
      node.addEventListener('blur', function () { hover(null); });
    });

    closeBtn.addEventListener('click', function () { close(true); });

    document.addEventListener('keydown', function (e) {
      if ((e.key === 'Escape' || e.key === 'Esc') && selected) {
        e.preventDefault();
        close(fig.contains(document.activeElement) || document.activeElement === document.body);
      }
    });

    // A click on the diagram's background closes the panel.
    svg.addEventListener('click', function (e) {
      if (selected && !e.target.closest('.arch-node')) close(false);
    });

    function edgesOf(id) {
      return edges.filter(function (g) {
        return g.dataset.from === id || g.dataset.to === id;
      });
    }

    // Highlights a node's edges (and scheduler jobs), drawn last in their
    // layer so they sit above the others.
    function mark(id, cls) {
      edges.forEach(function (g) { g.classList.remove(cls); });
      if (!id) return;
      edgesOf(id).forEach(function (g) {
        g.classList.add(cls);
        g.parentNode.appendChild(g);
      });
    }

    function hover(id) {
      if (selected) return; // the selection's highlight stays put
      mark(id, 'is-on');
    }

    function toggle(node, fromKeyboard) {
      if (selected === node) {
        close(fromKeyboard);
      } else {
        open(node, fromKeyboard);
      }
    }

    function open(node, fromKeyboard) {
      var id = node.dataset.id;
      var entry = document.getElementById('arch-c-' + id);
      if (!entry) return;

      if (selected) {
        selected.classList.remove('is-selected');
        selected.setAttribute('aria-expanded', 'false');
      }
      selected = node;
      node.classList.add('is-selected');
      node.setAttribute('aria-expanded', 'true');
      fig.classList.add('has-selection');

      var related = {};
      related[id] = true;
      edgesOf(id).forEach(function (g) {
        related[g.dataset.from] = true;
        related[g.dataset.to] = true;
      });
      nodes.forEach(function (n) {
        n.classList.toggle('is-related', !!related[n.dataset.id] && n !== node);
      });
      mark(id, 'is-on');

      title.textContent = entry.dataset.title;
      kind.textContent = entry.dataset.kind;
      var src = entry.querySelector('.arch-item-body');
      body.innerHTML = src ? src.innerHTML : '';

      // Put the panel on the side away from the node, so the node and most
      // of its edges stay in view.
      var box = node.getBBox();
      var vb = svg.viewBox.baseVal;
      panel.dataset.side = box.x + box.width / 2 > vb.width / 2 ? 'left' : 'right';

      panel.hidden = false;
      panel.scrollTop = 0;
      status.textContent = 'Showing details for ' + entry.dataset.title + '.';

      var overlay = getComputedStyle(panel).position === 'absolute';
      if (fromKeyboard) {
        title.focus({ preventScroll: true });
      }
      if (!overlay) {
        panel.scrollIntoView({ block: 'nearest', behavior: reduceMotion ? 'auto' : 'smooth' });
      }
    }

    function close(restoreFocus) {
      if (!selected) return;
      var node = selected;
      selected = null;
      node.classList.remove('is-selected');
      node.setAttribute('aria-expanded', 'false');
      fig.classList.remove('has-selection');
      nodes.forEach(function (n) { n.classList.remove('is-related'); });
      mark(null, 'is-on');
      panel.hidden = true;
      status.textContent = 'Details closed.';
      if (restoreFocus) node.focus();
    }
  }

  function start() {
    Array.prototype.forEach.call(document.querySelectorAll('[data-arch]'), init);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start);
  } else {
    start();
  }
})();
