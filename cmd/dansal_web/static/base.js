// Extracted from base.html's inline <script> blocks (#1217) — the
// data-fn dispatcher, shared helper functions, and help-modal logic
// used across every page. Cached by the browser across page
// navigations instead of being re-downloaded as inline HTML.
//
// Loaded synchronously (no defer) and at the same position base.html
// always loaded it: many page templates (location.html, event.html,
// org.html, festivals.html, and others) call dansalLeafletCss()/
// attachTileLayer()/renderMiniCalendar() from their own inline,
// non-deferred <script> blocks further down the page, expecting
// these functions to already be defined — a deferred load would
// execute after those call sites run and break every one of them.

// --- Delegated event-handler dispatcher (#1149) ---
// Elements use data-fn/data-args instead of inline onclick=/onchange=/
// onsubmit=/oninput=/onkeydown=/onfocus=/onload= so CSP can drop
// 'unsafe-inline' from script-src once #1141 flips the header (a nonce in
// script-src makes CSP-compliant browsers ignore 'unsafe-inline' entirely,
// so those attributes would otherwise silently stop firing). One
// document-level, capture-phase listener per event kind replaces every
// inline handler (capture phase works even for non-bubbling kinds like
// focus/load, since capture always travels down to the target regardless
// of bubbling); attributes:
//   data-fn="name"        function on window to call (dotted paths like
//                          "obj.method" are resolved against window)
//   data-args='[...]'     JSON array of arguments (built by the `dargs`
//                          template func); "@this"/"@value"/"@event" tokens
//                          are replaced with the element/its value/the event
//   data-confirm="text"   confirm() first; abort (and preventDefault) if declined
//   data-stop             call event.stopPropagation() (independent of
//                          data-on — matches a separately-attached ancestor
//                          listener, e.g. a table row's own click handler)
//   data-if-value         only dispatch when the element's value is truthy
//                          (the onchange="if(this.value)fn(...)" pattern)
//   data-self-only         only dispatch when the event target IS this element,
//                          not a descendant (modal-backdrop click-outside-closes)
//   data-on="change|submit|input|keydown|focus|load" dispatch data-fn/
//                          data-confirm on that event kind instead of the
//                          default "click" (data-stop always
//                          applies to click regardless of data-on)
function csp1149DispatchOne(el, evt, kind, suf) {
  var onAttr = el.getAttribute('data-on' + suf);
  var on = onAttr || (suf === '' ? 'click' : null);
  if (!on || on.split(',').indexOf(kind) === -1) return;
  if (el.hasAttribute('data-self-only') && evt.target !== el) return;
  if (el.hasAttribute('data-if-value') && !el.value) return;
  var confirmMsg = el.getAttribute('data-confirm' + suf);
  if (confirmMsg !== null && !confirm(confirmMsg)) {
    evt.preventDefault();
    return;
  }
  var fnName = el.getAttribute('data-fn' + suf);
  if (!fnName) return;
  var fn = fnName.split('.').reduce(function (o, k) { return o ? o[k] : undefined; }, window);
  if (typeof fn !== 'function') { console.error('csp1149Dispatch: unknown function', fnName); return; }
  var args = [];
  var raw = el.getAttribute('data-args' + suf);
  try { args = raw ? JSON.parse(raw) : []; } catch (err) { args = []; }
  args = args.map(function (a) {
    if (a === '@this') return el;
    if (a === '@value') return el.value;
    if (a === '@event') return evt;
    return a;
  });
  var result = fn.apply(el, args);
  if (kind === 'submit' && result === false) evt.preventDefault();
}
// A single element can carry up to three independent dispatch rules
// (data-fn/data-args/data-on, then data-fn2/data-args2/data-on2, then
// .../3) for the rare case where different event kinds on the same element
// need different functions or arguments (e.g. oninput vs onfocus vs
// onkeydown on one autocomplete box). data-stop/data-self-only/data-if-value
// are shared guards, not per-rule.
function csp1149Dispatch(el, evt, kind) {
  if (el.hasAttribute('data-stop')) evt.stopPropagation();
  ['', '2', '3'].forEach(function (suf) { csp1149DispatchOne(el, evt, kind, suf); });
}
['click', 'change', 'submit', 'input', 'keydown', 'focus', 'load'].forEach(function (kind) {
  document.addEventListener(kind, function (e) {
    // e.target isn't always an Element with .closest(): some browsers fire
    // events targeting document/window itself (observed with <input
    // type=color>'s native picker), and .closest doesn't exist on those —
    // throwing here would silently swallow this one event's dispatch
    // (the exception aborts this listener before reaching the data-fn
    // button the click was actually meant for), not just this diagnostic.
    if (!(e.target instanceof Element)) return;
    var el = e.target.closest('[data-fn],[data-stop],[data-confirm]');
    if (el) csp1149Dispatch(el, e, kind);
  }, true);
});
// navigateTo: small data-fn helper for whole-card/whole-row "click to open"
// elements (e.g. the mobile map popup in index.html).
function navigateTo(url) { window.location = url; }
// submitParentForm: data-fn helper for the very common auto-submit-filter
// pattern (data-on="change" data-fn="submitParentForm" data-args='["@this"]').
function submitParentForm(el) { el.form.submit(); }
function scrollToTop() { window.scrollTo({top: 0, behavior: 'smooth'}); }
// clickElement: data-fn helper for onclick="document.getElementById(id).click()".
function clickElement(id) { document.getElementById(id).click(); }
// hideParentElement: data-fn helper for onclick="this.parentElement.hidden=true".
function hideParentElement(el) { el.parentElement.hidden = true; }
// shareURL: data-fn helper for a share button — Web Share API where available
// (opens the native OS share sheet), else copies the link to the clipboard
// with a brief checkmark confirmation on the triggering button (same pattern
// as the copy-link buttons on admin_series_edit.html/admin_fetchurl_edit.html).
function shareURL(url, btn) {
  if (navigator.share) { navigator.share({url: url}).catch(function(){}); return; }
  if (navigator.clipboard) {
    navigator.clipboard.writeText(url).then(function(){
      var orig = btn.textContent;
      btn.textContent = '✓';
      setTimeout(function(){ btn.textContent = orig; }, 1500);
    });
  }
}
// closeBulkAttrsDialog: shared by the admin events/locations/org bulk-attrs modal.
function closeBulkAttrsDialog() { document.getElementById('bulk-attrs-dialog').close(); }
// toggleKuferFields: shared by the fetchurl new/edit forms' "type" select.
function toggleKuferFields(val) {
  document.getElementById('kufer-fields').style.display = val === 'kufer' ? '' : 'none';
}
function closeWindow() { window.close(); }

// --- New menu toggle functions ---
function toggleNavMenu(){
  var dropdown=document.getElementById('nav-dropdown');
  var isOpen=!dropdown.hidden;
  dropdown.hidden=isOpen;
  // Close user dropdown when opening nav
  if(!isOpen) document.getElementById('user-dropdown').hidden=true;
}
function toggleUserMenu(){
  var dropdown=document.getElementById('user-dropdown');
  var isOpen=!dropdown.hidden;
  dropdown.hidden=isOpen;
  // Close nav dropdown when opening user
  if(!isOpen) document.getElementById('nav-dropdown').hidden=true;
}
// Close dropdowns when clicking outside
document.addEventListener('click',function(e){
  // e.target isn't always an Element with .closest() — see the capture-phase
  // dispatcher above for why (observed with <input type=color>'s native
  // picker); guard the same way here.
  if(!(e.target instanceof Element) || !e.target.closest('nav')){
    document.getElementById('nav-dropdown').hidden=true;
    document.getElementById('user-dropdown').hidden=true;
  }
});
// Close on Escape key
document.addEventListener('keydown',function(e){
  if(e.key==='Escape'){
    document.getElementById('nav-dropdown').hidden=true;
    document.getElementById('user-dropdown').hidden=true;
    document.getElementById('info-popup').hidden=true;
  }
});

function initSortable(tbl){
  var ths=tbl.querySelectorAll('thead th[data-sort]');
  ths.forEach(function(th){
    th.classList.add('sortable-th');
    th.addEventListener('click',function(){
      var asc=th.dataset.sortDir!=='asc';
      ths.forEach(function(t){t.dataset.sortDir='';var i=t.querySelector('.sort-ind');if(i)i.textContent='';});
      th.dataset.sortDir=asc?'asc':'desc';
      var ind=th.querySelector('.sort-ind');
      if(!ind){ind=document.createElement('span');ind.className='sort-ind';th.appendChild(ind);}
      ind.textContent=asc?' ▲':' ▼';
      var tbody=tbl.querySelector('tbody');
      var rows=Array.prototype.slice.call(tbody.querySelectorAll('tr'));
      var allThs=Array.prototype.slice.call(tbl.querySelectorAll('thead th'));
      var col=allThs.indexOf(th);
      var type=th.dataset.sort;
      rows.sort(function(a,b){
        var av=a.cells[col]?(a.cells[col].dataset.val||a.cells[col].textContent.trim()):'';
        var bv=b.cells[col]?(b.cells[col].dataset.val||b.cells[col].textContent.trim()):'';
        if(type==='num'){return asc?(+av||0)-(+bv||0):(+bv||0)-(+av||0);}
        return asc?av.localeCompare(bv):bv.localeCompare(av);
      });
      rows.forEach(function(r){tbody.appendChild(r);});
    });
  });
}
function toggleHamburger(btn){
  var nav=btn.closest('nav');
  var open=nav.classList.toggle('nav-open');
  btn.textContent=open?'✕':'☰';
  btn.setAttribute('aria-expanded',open);
}
function toggleInfoPopup(btn){
  var p=btn.nextElementSibling;
  var open=!p.hidden;
  p.hidden=open;
  if(!open){document.addEventListener('click',function h(e){if(!btn.parentElement.contains(e.target)){p.hidden=true;document.removeEventListener('click',h);}},{once:false,capture:false});}
}
function cycleTheme(){
  var h=document.documentElement;
  var cur=localStorage.getItem('colorScheme')||'auto';
  var next=cur==='auto'?'dark':cur==='dark'?'light':'auto';
  localStorage.setItem('colorScheme',next);
  h.classList.remove('dark','light');
  if(next==='dark')h.classList.add('dark');
  else if(next==='light')h.classList.add('light');
}
// Tiles are proxied and disk-cached through dansal_web itself (#1079) rather
// than fetched by the browser directly from OSM — both a tile usage policy
// requirement (OSM forbids heavy direct use by distributed apps) and a
// privacy win (visitor IPs never reach a third-party tile server).
// #1169: dark mode used to switch to a separate CARTO "dark_all" upstream,
// which now requires a registered API key and serves an "API key required"
// overlay without one. Dark mode now reuses these same OSM tiles and is
// darkened purely with a CSS filter on the tile pane (see base.html's
// .leaflet-tile-pane rule) — no second upstream, no swap needed on theme
// change.
function makeTileLayer(){
  return L.tileLayer('/tiles/osm/{z}/{x}/{y}.png',{
    attribution:'© <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>',
    maxZoom:18});
}
// Nominatim address results are re-fetched in the venue's own country's
// language, so a German venue always gets "München"/"Bayern" etc. regardless
// of the admin's own UI language or the browser's locale (#1126). Keyed on
// ISO 3166-1 alpha-2 country code; falls back to English for anything not
// listed rather than guessing a language.
var NOMINATIM_REGION_LANG = {
  DE:'de', AT:'de', CH:'de,fr,it', LI:'de',
  FR:'fr', BE:'nl,fr', LU:'fr,de', MC:'fr',
  NL:'nl', ES:'es', PT:'pt', AD:'ca',
  IT:'it', SM:'it', VA:'it',
  DK:'da', SE:'sv', NO:'no', FI:'fi,sv',
  PL:'pl', CZ:'cs', SK:'sk', HU:'hu', RO:'ro'
};
function nominatimLang(countryCode){
  return NOMINATIM_REGION_LANG[(countryCode||'').toUpperCase()] || 'en';
}
// Re-fetches Nominatim's reverse endpoint for (lat, lon) in the country's own
// language and hands the resulting `address` object to cb (null on failure).
// Callers use this as a second pass once an initial search/reverse call (in
// whatever language) has revealed the country_code, so town/country/region
// end up in the local language instead of whatever language the first,
// necessarily-blind call happened to use.
function nominatimRefine(lat, lon, countryCode, cb){
  fetch('https://nominatim.openstreetmap.org/reverse?format=json&addressdetails=1&lat=' + lat + '&lon=' + lon, {
    headers: {'Accept-Language': nominatimLang(countryCode)}
  }).then(function(r){ return r.json(); }).then(function(item){
    cb((item && item.address) || null);
  }).catch(function(){ cb(null); });
}
// renderMiniCalendar draws a small multi-month event calendar into
// #containerId: one grid per month in `months` (array of Date, each the
// 1st of a month to render), days with an event highlighted and linking
// to /events/{id}. calEvents is the [{id,t,s,e}] shape produced by the
// festivalCalendarJSON template func. Shared by /festivals (fixed
// calendar year, its own month list) and /org/{slug} (#1161, a rolling
// window of upcoming months) so this rendering logic isn't duplicated.
function renderMiniCalendar(containerId, calEvents, months, opts){
  opts=opts||{};
  var cal=document.getElementById(containerId);
  if(!cal) return;
  if(!calEvents.length || !months.length){
    cal.hidden=true;
    if(opts.wrapId){var w=document.getElementById(opts.wrapId); if(w) w.hidden=true;}
    return;
  }
  var lang=document.documentElement.lang||'de';
  var fmtMonth=new Intl.DateTimeFormat(lang,{month:'long',year:'numeric'});
  var fmtWd=new Intl.DateTimeFormat(lang,{weekday:'narrow'});
  function dayKey(d){return d.getFullYear()+'-'+(d.getMonth()+1)+'-'+d.getDate();}
  // Map every day covered by an event (start..end) to the event shown there.
  var dayMap={};
  calEvents.forEach(function(ev){
    var start=new Date(ev.s);
    var end=ev.e?new Date(ev.e):start;
    var d=new Date(start.getFullYear(),start.getMonth(),start.getDate());
    var last=new Date(end.getFullYear(),end.getMonth(),end.getDate());
    while(d<=last){dayMap[dayKey(d)]=ev;d.setDate(d.getDate()+1);}
  });
  var html='';
  months.forEach(function(monthDate){
    var year=monthDate.getFullYear(), month=monthDate.getMonth();
    html+='<div class="fest-month"><div class="fest-month-title">'+fmtMonth.format(monthDate)+'</div>';
    html+='<table class="fest-month-grid"><thead><tr>';
    var wd0=new Date(year,month,1);
    var dow0=wd0.getDay();
    wd0.setDate(wd0.getDate()-(dow0===0?6:dow0-1));
    for(var i=0;i<7;i++){var hd=new Date(wd0);hd.setDate(hd.getDate()+i);html+='<th>'+fmtWd.format(hd)+'</th>';}
    html+='</tr></thead><tbody>';
    var cur=new Date(wd0);
    for(var row=0;row<6;row++){
      html+='<tr>';
      for(var col=0;col<7;col++){
        var inMonth=cur.getMonth()===month;
        var ev=dayMap[dayKey(cur)];
        if(ev){
          html+='<td class="fest-day" data-href="/events/'+ev.id+'" title="'+ev.t.replace(/"/g,'&quot;')+'">'+cur.getDate()+'</td>';
        } else {
          html+='<td class="'+(inMonth?'':'fest-other')+'">'+cur.getDate()+'</td>';
        }
        cur.setDate(cur.getDate()+1);
      }
      html+='</tr>';
      if(row>=3 && cur.getMonth()!==month) break;
    }
    html+='</tbody></table></div>';
  });
  cal.innerHTML=html;
  cal.hidden=false;
  cal.querySelectorAll('td.fest-day').forEach(function(td){
    td.addEventListener('click',function(){window.location.href=this.dataset.href;});
  });
  if(opts.carousel){
    cal.classList.add('fest-carousel');
    var prevBtn=opts.prevId&&document.getElementById(opts.prevId);
    var nextBtn=opts.nextId&&document.getElementById(opts.nextId);
    function step(){
      var m=cal.querySelector('.fest-month');
      return m?m.getBoundingClientRect().width+16:cal.clientWidth;
    }
    function updateBtns(){
      if(prevBtn) prevBtn.disabled=cal.scrollLeft<=2;
      if(nextBtn) nextBtn.disabled=cal.scrollLeft+cal.clientWidth>=cal.scrollWidth-2;
    }
    if(prevBtn) prevBtn.onclick=function(){cal.scrollBy({left:-step(),behavior:'smooth'});};
    if(nextBtn) nextBtn.onclick=function(){cal.scrollBy({left:step(),behavior:'smooth'});};
    cal.addEventListener('scroll',updateBtns);
    updateBtns();
    // #1171: touch swipe, same threshold/pattern as the homepage week-table
    // swipe (index.html). overflow-x is hidden (paging is arrow/swipe-only,
    // no visible scrollbar drag), so without this a touch drag would do
    // nothing on mobile — the arrows alone aren't the "swipe like the main
    // page" the carousel was expected to support.
    var sx=null;
    cal.addEventListener('touchstart',function(e){sx=e.touches[0].clientX;},{passive:true});
    cal.addEventListener('touchend',function(e){
      if(sx===null)return;
      var dx=e.changedTouches[0].clientX-sx; sx=null;
      if(Math.abs(dx)>50) cal.scrollBy({left:dx<0?step():-step(),behavior:'smooth'});
    },{passive:true});
  }
}
function attachTileLayer(map){
  // #1169: light and dark now share one tile layer (CSS filter handles the
  // dark styling), so there's no swap-on-theme-change to observe any more.
  makeTileLayer().addTo(map);
  fixDefaultMarkerIcon();
}
function fixDefaultMarkerIcon(){
  // #1221: plain L.marker(...) calls (no explicit icon:) use Leaflet's
  // built-in default pin, whose image path Leaflet detects at runtime by
  // reading a `.leaflet-default-icon-path` background-image rule out of
  // leaflet.css — and caches whatever it finds (even nothing) for the rest
  // of the page's life. dansalLeafletCss() loads that CSS non-blocking
  // (media="print" flipped to "all" on load), so a page that creates its
  // first default marker synchronously right after calling it — before the
  // stylesheet has actually applied — gets a failed detection and every
  // default marker on the page renders as a broken-image glyph instead of
  // a pin. Set the URLs explicitly once so there's nothing to race.
  //
  // IconDefault._getIconUrl() (Leaflet's internal, not overridden here)
  // computes `(this.options.imagePath || IconDefault.imagePath) +
  // <name>Url`, i.e. it PREPENDS a separately-detected `imagePath` in
  // front of options.iconUrl -- mergeOptions() below only sets the latter.
  // Left alone, imagePath detection still runs (and, since
  // dansalLeafletCss() has already inserted the <link href=".../
  // leaflet.css"> element by the time a marker is created, its
  // querySelector('link[href$="leaflet.css"]') fallback finds it even
  // while media="print"), so the absolute URLs below would get a bogus
  // prefix concatenated onto them. Pin imagePath to '' too so detection
  // is skipped and the absolute URLs are used as-is.
  if(fixDefaultMarkerIcon.done) return;
  fixDefaultMarkerIcon.done=true;
  L.Icon.Default.imagePath='';
  L.Icon.Default.mergeOptions({
    iconUrl:'https://unpkg.com/leaflet@1.9.4/dist/images/marker-icon.png',
    iconRetinaUrl:'https://unpkg.com/leaflet@1.9.4/dist/images/marker-icon-2x.png',
    shadowUrl:'https://unpkg.com/leaflet@1.9.4/dist/images/marker-shadow.png'
  });
}
function dansalLeafletCss(cluster){
  // Pagespeed's "avoid chaining critical requests" audit: Leaflet's
  // stylesheets used to be render-blocking <link>s in <head>, putting ~4KB of
  // CDN CSS on the critical path of every map page and delaying first paint.
  // Load them non-blocking instead — fetch with media="print", flip to "all"
  // once loaded. CSP blocks an inline onload= on the <link>, so the flip
  // happens here via the property (which the nonce'd script may set).
  if(dansalLeafletCss.done) return;
  dansalLeafletCss.done=true;
  var sheets=[['https://unpkg.com/leaflet@1.9.4/dist/leaflet.css','sha384-sHL9NAb7lN7rfvG5lfHpm643Xkcjzp4jFvuavGOndn6pjVqS6ny56CAt3nsEVT4H']];
  if(cluster) sheets.push(['https://unpkg.com/leaflet.markercluster@1.5.3/dist/MarkerCluster.Default.css','sha384-wgw+aLYNQ7dlhK47ZPK7FRACiq7ROZwgFNg0m04avm4CaXS+Z9Y7nMu8yNjBKYC+']);
  sheets.forEach(function(s){
    var l=document.createElement('link');
    l.rel='stylesheet';l.href=s[0];l.integrity=s[1];l.crossOrigin='anonymous';l.media='print';
    l.onload=function(){l.media='all';};
    document.head.appendChild(l);
  });
}
(function(){
  var btn=document.getElementById('back-to-top');
  var nav=document.querySelector('nav');
  if(!btn||!nav)return;
  new IntersectionObserver(function(e){btn.classList.toggle('visible',!e[0].isIntersecting);}).observe(nav);
})();
var _langPending=null;
function handleLangChange(sel){
  var code=sel.value;
  var hasCookie=document.cookie.split(';').some(function(c){return c.trim().startsWith('dsw_lang=');});
  if(hasCookie){location.href='/lang?code='+code;return;}
  _langPending=code;
  document.getElementById('lang-consent-modal').hidden=false;
}
function langConsentAccept(){
  document.getElementById('lang-consent-modal').hidden=true;
  if(_langPending){location.href='/lang?code='+_langPending;}
}
function langConsentDeny(){
  document.getElementById('lang-consent-modal').hidden=true;
  var sel=document.getElementById('lang-select');
  if(sel){var opts=sel.options;for(var i=0;i<opts.length;i++){if(opts[i].defaultSelected){sel.selectedIndex=i;break;}}}
}

// initDateRangePicker — reusable two-click date-range calendar popup.
// opts: {btn, popup, grid, title, prev, next, from, to, placeholder, onSelect}
// onSelect(fromISO, toISO) is called when the user completes a selection.
// Same-day: first click = half-circle; second click on same date = full circle.
function initDateRangePicker(opts){
  var pendingStart=null;
  var from=opts.from||'', to=opts.to||'';
  var calYear, calMonth;
  function pad(n){return n<10?'0'+n:''+n;}
  function iso(d){return d.getFullYear()+'-'+pad(d.getMonth()+1)+'-'+pad(d.getDate());}
  function updateBtn(){
    opts.btn.textContent = from&&to ? (from===to ? from : from+' → '+to) : (opts.placeholder||'📅');
  }
  updateBtn();
  function render(){
    var lang=document.documentElement.lang||'de';
    var fmtMY=new Intl.DateTimeFormat(lang,{month:'long',year:'numeric'});
    var fmtWd=new Intl.DateTimeFormat(lang,{weekday:'narrow'});
    opts.title.textContent=fmtMY.format(new Date(calYear,calMonth,1));
    var cur=new Date(calYear,calMonth,1);
    var dow=cur.getDay(); cur.setDate(cur.getDate()-(dow===0?6:dow-1));
    var selS=pendingStart||from, selE=pendingStart?pendingStart:to;
    if(selS>selE){var t=selS;selS=selE;selE=t;}
    var h='<thead><tr>';
    for(var i=0;i<7;i++){var hd=new Date(cur);hd.setDate(hd.getDate()+i);h+='<th>'+fmtWd.format(hd)+'</th>';}
    h+='</tr></thead><tbody>';
    for(var row=0;row<6;row++){
      h+='<tr>';
      for(var col=0;col<7;col++){
        var d=iso(cur);
        var cls=cur.getMonth()!==calMonth?'dpr-other':'';
        if(!pendingStart&&d>=selS&&d<=selE) cls+=' dpr-in-range';
        if(d===selS) cls+=' dpr-range-start';
        if(d===selE&&!pendingStart) cls+=' dpr-range-end';
        if(pendingStart&&d===pendingStart) cls+=' dpr-range-start dpr-range-end';
        h+='<td class="'+cls.trim()+'" data-iso="'+d+'">'+cur.getDate()+'</td>';
        cur.setDate(cur.getDate()+1);
      }
      h+='</tr>';
      if(row>=3&&cur.getMonth()!==calMonth) break;
    }
    h+='</tbody>';
    opts.grid.innerHTML=h;
    opts.grid.querySelectorAll('td').forEach(function(td){
      td.addEventListener('click',function(){
        var d=this.dataset.iso;
        if(!pendingStart){
          pendingStart=d;
          setTimeout(render,0);
        } else {
          var s=pendingStart,e=d;
          if(e<s){var t=s;s=e;e=t;}
          pendingStart=null;
          from=s; to=e;
          updateBtn();
          opts.popup.hidden=true;
          opts.onSelect(from,to);
        }
      });
    });
  }
  opts.btn.addEventListener('click',function(){
    var d=new Date((from||iso(new Date()))+'T00:00:00');
    calYear=d.getFullYear(); calMonth=d.getMonth();
    pendingStart=null;
    opts.popup.hidden=!opts.popup.hidden;
    if(!opts.popup.hidden) render();
  });
  opts.prev.addEventListener('click',function(e){
    e.preventDefault();
    e.stopPropagation();
    if(--calMonth<0){calMonth=11;calYear--;}
    render();
  });
  opts.next.addEventListener('click',function(e){
    e.preventDefault();
    e.stopPropagation();
    if(++calMonth>11){calMonth=0;calYear++;}
    render();
  });
  document.addEventListener('click',function(e){
    if(!opts.popup.hidden&&!opts.popup.contains(e.target)&&e.target!==opts.btn)
      opts.popup.hidden=true;
  });
  // Adopt a programmatically-set range (e.g. wizard prefill) so the button
  // label, calendar default month and range highlight all stay in sync.
  opts.setRange = function(f, t) {
    from = f || '';
    to = t || from;
    updateBtn();
  };
  return opts;
}

function openHelpModal(){document.getElementById('hm-backdrop').hidden=false;}
function closeHelpModal(){document.getElementById('hm-backdrop').hidden=true;}
document.addEventListener('keydown',function(e){
  if(e.key==='Escape'){closeHelpModal();return;}
  if(e.key==='?'&&!e.ctrlKey&&!e.metaKey&&!e.altKey&&!['INPUT','TEXTAREA','SELECT'].includes(document.activeElement.tagName)){
    var el=document.getElementById('hm-backdrop');
    el.hidden?openHelpModal():closeHelpModal();
  }
});
