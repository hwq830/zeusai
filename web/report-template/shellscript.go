package reporttemplate

// visualShellScript is the template's shell injection block plus the report
// source-panel glue. It is emitted as trusted JS so the pinned CSP hash covers
// exactly these bytes.
const visualShellScript = `/* 目录跳转 + 浮动目录抽屉（右侧固定，仅纵向拖动） */
(function(){
  var reduce = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  function curY(){ return window.pageYOffset || document.documentElement.scrollTop || 0; }
  function syncHash(id){ try { history.replaceState(null, '', '#' + id); } catch(e){} }

  function jumpTo(el){
    var y = el.getBoundingClientRect().top + curY() - 16;
    if (y < 0) y = 0;
    var root = document.documentElement, prev = root.style.scrollBehavior;
    root.style.scrollBehavior = 'auto';
    window.scrollTo(0, y);
    setTimeout(function(){ root.style.scrollBehavior = prev; }, 0);
  }
  function go(id){
    var el = document.getElementById(id);
    if (!el) return false;
    if (reduce || Math.abs(el.getBoundingClientRect().top) > window.innerHeight * 1.5) {
      jumpTo(el); return true;
    }
    var start = curY();
    try { el.scrollIntoView({behavior:'smooth', block:'start'}); }
    catch(e){ jumpTo(el); return true; }
    setTimeout(function(){ if (Math.abs(curY() - start) < 8) jumpTo(el); }, 420);
    return true;
  }

  /* ---------- 侧栏目录 ---------- */
  var sideLinks = document.querySelectorAll('#nav a[href^="#"]');
  for (var i = 0; i < sideLinks.length; i++) {
    sideLinks[i].addEventListener('click', function(e){
      var id = this.getAttribute('href').slice(1);
      if (!go(id)) return;
      e.preventDefault(); syncHash(id);
    });
  }

  /* ---------- 正文引用上标 ---------- */
  function flash(el){
    if (!el) return;
    el.classList.add('flash');
    setTimeout(function(){ el.classList.remove('flash'); }, 1600);
  }
  var citeLinks = document.querySelectorAll('a[href^="#ref-"]');
  for (var c = 0; c < citeLinks.length; c++) {
    citeLinks[c].addEventListener('click', function(e){
      var id = this.getAttribute('href').slice(1);
      var target = document.getElementById(id);
      if (!go(id)) return;
      e.preventDefault(); syncHash(id);
      flash(target);
    });
  }

  /* ---------- 抽屉 ---------- */
  var fab = document.getElementById('tocFab'),
      drawer = document.getElementById('tocDrawer'),
      list = document.getElementById('tocList'),
      nav = document.getElementById('nav');
  if (!fab || !drawer || !list || !nav) return;
  if (list.getAttribute('data-built') === '1') return;
  list.setAttribute('data-built','1');

  var isOpen = false;

  (function build(){
    var src = nav.querySelectorAll('a[href^="#"]');
    for (var j = 0; j < src.length; j++) {
      var a = src[j].cloneNode(true);
      a.classList.remove('on');
      a.addEventListener('click', function(e){
        e.preventDefault();
        var id = this.getAttribute('href').slice(1);
        close(false);
        go(id); syncHash(id);
      });
      list.appendChild(a);
    }
  })();

  function markActive(){
    var act = nav.querySelector('a.on');
    var href = act ? act.getAttribute('href') : '';
    var items = list.querySelectorAll('a');
    for (var k = 0; k < items.length; k++) {
      items[k].classList.toggle('on', items[k].getAttribute('href') === href);
    }
  }
  function open(){
    if (isOpen) return;
    isOpen = true;
    markActive();
    drawer.classList.add('open');
    drawer.setAttribute('aria-hidden','false');
    fab.setAttribute('aria-expanded','true');
    document.documentElement.style.overflow = 'hidden';
    document.body.classList.add('toc-locked');
    setTimeout(function(){ var c = drawer.querySelector('.toc-close'); if (c) c.focus(); }, 70);
  }
  function close(returnFocus){
    if (!isOpen) return;
    isOpen = false;
    drawer.classList.remove('open');
    drawer.setAttribute('aria-hidden','true');
    fab.setAttribute('aria-expanded','false');
    document.documentElement.style.overflow = '';
    document.body.classList.remove('toc-locked');
    if (returnFocus !== false) fab.focus();
  }

  /* ---------- 纵向拖动（X 轴锁定在 CSS 的 right 上） ---------- */
  var EDGE = 8, SIZE = 46, DEFAULT_B = 22;
  var lastDragEnd = 0;

  function clampB(b){
    var maxB = window.innerHeight - SIZE - EDGE;
    return Math.max(EDGE, Math.min(b, Math.max(EDGE, maxB)));
  }
  function setBottom(b){
    var v = clampB(b);
    fab.style.bottom = v + 'px';
    return v;
  }
  function readBottom(){
    var b = parseFloat(fab.style.bottom);
    return isNaN(b) ? DEFAULT_B : b;
  }
  function saveY(b){ try { localStorage.setItem('tocFabY', String(b)); } catch(e){} }
  function loadY(){
    try {
      var s = localStorage.getItem('tocFabY');
      if (s === null) return null;
      var n = parseFloat(s);
      return isNaN(n) ? null : n;
    } catch(e){ return null; }
  }
  (function restore(){ var y = loadY(); if (y !== null) setBottom(y); })();

  var drag = null;
  fab.addEventListener('pointerdown', function(e){
    if (e.button && e.button !== 0) return;
    var box = fab.getBoundingClientRect();
    drag = { x: e.clientX, y: e.clientY, top: box.top, moved: false };
    fab.classList.add('dragging');
    try { fab.setPointerCapture(e.pointerId); } catch(err){}
  });
  fab.addEventListener('pointermove', function(e){
    if (!drag) return;
    var dx = e.clientX - drag.x, dy = e.clientY - drag.y;
    if (!drag.moved && Math.abs(dx) < 5 && Math.abs(dy) < 5) return;
    drag.moved = true;
    setBottom(window.innerHeight - (drag.top + dy) - SIZE);   /* 只用 dy，X 不参与 */
  });
  function endDrag(e){
    if (!drag) return;
    fab.classList.remove('dragging');
    try { fab.releasePointerCapture(e.pointerId); } catch(err){}
    if (drag.moved) { lastDragEnd = Date.now(); saveY(readBottom()); }
    drag = null;
  }
  fab.addEventListener('pointerup', endDrag);
  fab.addEventListener('pointercancel', endDrag);

  fab.addEventListener('click', function(){
    if (Date.now() - lastDragEnd < 300) return;
    isOpen ? close() : open();
  });

  var closers = drawer.querySelectorAll('[data-toc-close]');
  for (var m = 0; m < closers.length; m++) {
    closers[m].addEventListener('click', function(){ close(); });
  }

  var topBtn = drawer.querySelector('[data-toc-top]');
  if (topBtn) {
    topBtn.addEventListener('click', function(){
      close(false);
      var root = document.documentElement, prev = root.style.scrollBehavior;
      root.style.scrollBehavior = 'auto';
      window.scrollTo(0, 0);
      setTimeout(function(){ root.style.scrollBehavior = prev; }, 0);
      try { history.replaceState(null, '', location.href.split('#')[0]); } catch(err){}
    });
  }
  document.addEventListener('keydown', function(e){
    if (isOpen && (e.key === 'Escape' || e.keyCode === 27)) close();
  });
  window.addEventListener('resize', function(){
    if (isOpen && window.innerWidth > 900) close(false);
    setBottom(readBottom());
  });
})();

(function(){var panel=document.querySelector(".c-source-panel"),shade=document.querySelector(".c-source-shade"),close=document.querySelector(".c-source-close"),title=document.querySelector(".c-source-title"),meta=document.querySelector(".c-source-meta"),link=document.querySelector(".c-source-original");if(!panel||!shade||!close)return;function hide(){panel.hidden=true;shade.hidden=true}document.querySelectorAll(".refs a.t[data-source-url]").forEach(function(a){a.addEventListener("click",function(){title.textContent=this.getAttribute("data-source-title")||this.textContent;meta.textContent=this.getAttribute("data-source-meta")||"";var u=this.getAttribute("data-source-url");link.hidden=!u;if(u)link.href=u;panel.hidden=false;shade.hidden=false;close.focus()})});close.addEventListener("click",hide);shade.addEventListener("click",hide);document.addEventListener("keydown",function(e){if(e.key==="Escape"&&!panel.hidden)hide()})})();`

const visualShellScriptHash = "DRlIu9gJp7UmunxaRWTuJKquIO1Y3XE9TYmRn/hqGV4="
