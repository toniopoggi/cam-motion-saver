'use strict';
window.CFN={};
const C=window.CFN;
C.el=id=>document.getElementById(id);
C.escape=s=>String(s==null?'':s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
C.icon=(name)=>{const paths={play:'<path d="m8 5 11 7-11 7z"/>',download:'<path d="M12 3v12m-4-4 4 4 4-4M5 16v4h14v-4"/>',camera:'<path d="M4 7h4l2-3h4l2 3h4v13H4z"/><circle cx="12" cy="13" r="4"/>',close:'<path d="m6 6 12 12M6 18 18 6"/>',refresh:'<path d="M20 5v5h-5M4 19v-5h5M5 8a8 8 0 0 1 13-3l2 5M4 14l2 5a8 8 0 0 0 13-3"/>'};return '<svg viewBox="0 0 24 24" aria-hidden="true">'+(paths[name]||paths.camera)+'</svg>'};
C.json=async(path,options={})=>{const r=await fetch(path,{...options,cache:'no-store'});let data;const body=await r.text();try{data=JSON.parse(body)}catch(_){throw Error(r.status===401?'This browser needs a fresh private link from the recording phone.':body.slice(0,180)||'Request failed.')}if(!r.ok)throw Error(data.error||'Request failed ('+r.status+').');return data};
C.post=(path,body={})=>C.json(path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
const currentURL=new URL(location.href);if(currentURL.searchParams.has('key')){currentURL.searchParams.delete('key');history.replaceState(null,'',currentURL.pathname+currentURL.search+currentURL.hash)}
C.viewer=document.body.dataset.page==='viewer';
let allClips=[],signature='',refreshBusy=false,returnFocus=null;
function duration(seconds){if(!seconds)return '';const n=Math.round(seconds);return Math.floor(n/60)+':'+String(n%60).padStart(2,'0')}
function clipDay(time){const t=new Date(time);const today=new Date();today.setHours(0,0,0,0);const yesterday=new Date(today);yesterday.setDate(yesterday.getDate()-1);return t>=today?'today':t>=yesterday?'yesterday':'older'}
function render(){const selected=C.el('dayFilter').value;const clips=allClips.filter(c=>selected==='all'||clipDay(c.Time)===selected);C.el('count').textContent=allClips.length?'('+allClips.length+')':'';
 C.el('clips').innerHTML=clips.length?clips.map(c=>{
  const name=C.escape(c.Name),path='/clip/'+encodeURIComponent(c.Name),playable=c.Name.endsWith('.mp4'),at=new Date(c.Time),time=at.toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'}),date=at.toLocaleDateString([],{day:'numeric',month:'short'});
  const preview=c.Preview?'<img src="'+C.escape(c.Preview)+'" alt="Preview of recording at '+C.escape(time)+'" loading="lazy" decoding="async">':'<span class="placeholder">'+C.icon('camera')+'<span>'+(c.PreviewStatus==='failed'?'Preview unavailable':c.PreviewStatus==='processing'?'Preparing preview':'Preview queued')+'</span></span>';
  return '<article class="clip-card"><a class="preview" href="'+path+'" '+(playable?'data-play="'+name+'"':'download')+' aria-label="Play recording from '+C.escape(date+' '+time)+'">'+preview+(playable?'<span class="play-mark">'+C.icon('play')+'</span>':'')+(c.Preview?'<span class="preview-label">3s preview</span>':'')+(c.DurationSeconds?'<span class="clip-duration">'+duration(c.DurationSeconds)+'</span>':'')+'</a><div class="clip-footer"><div><strong>'+C.escape(time)+'</strong><small>'+C.escape(date)+' · '+(c.Size/1048576).toFixed(1)+' MB</small><span class="clip-name">'+name+'</span></div><a class="icon-button" href="'+path+'?download=1" download aria-label="Download recording from '+C.escape(time)+'">'+C.icon('download')+'</a></div>'+(!C.viewer&&!c.Preview&&c.PreviewStatus==='failed'?'<div class="preview-error"><button class="text" data-retry="'+name+'">Retry preview</button></div>':'')+'</article>';
 }).join(''):'<div class="empty"><img src="/assets/icon-192.png" alt=""><strong>'+(allClips.length?'No clips for this day':'No recordings yet')+'</strong><p>'+(allClips.length?'Choose another day above.':'New clips will appear here.')+'</p></div>';
}
C.loadClips=async()=>{const clips=await C.json('/api/clips');const next=JSON.stringify(clips);if(next!==signature){signature=next;allClips=clips;render()}C.el('galleryMessage').textContent=''};
C.el('dayFilter').onchange=render;
C.el('refreshClips').innerHTML=C.icon('refresh');
C.el('refreshClips').onclick=async()=>{try{await C.loadClips()}catch(e){C.el('galleryMessage').textContent=e.message}};
function closePlayer(){const video=C.el('video');video.pause();video.removeAttribute('src');video.load();C.el('player').hidden=true;document.body.style.overflow='';if(returnFocus&&returnFocus.isConnected)returnFocus.focus()}
C.el('closePlayer').innerHTML=C.icon('close');C.el('downloadVideo').innerHTML=C.icon('download');C.el('closePlayer').onclick=closePlayer;
C.el('player').onclick=e=>{if(e.target===C.el('player'))closePlayer()};
document.addEventListener('keydown',e=>{if(C.el('player').hidden)return;if(e.key==='Escape')closePlayer();if(e.key==='Tab'){const nodes=[C.el('downloadVideo'),C.el('closePlayer'),C.el('video')];const first=nodes[0],last=nodes[2];if(e.shiftKey&&document.activeElement===first){e.preventDefault();last.focus()}else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first.focus()}}});
document.addEventListener('click',async e=>{const play=e.target.closest('[data-play]');if(play){e.preventDefault();returnFocus=play;const name=play.dataset.play,path='/clip/'+encodeURIComponent(name);C.el('playerTitle').textContent='Recording · '+name;C.el('downloadVideo').href=path+'?download=1';C.el('player').hidden=false;document.body.style.overflow='hidden';C.el('video').src=path;C.el('video').play().catch(()=>{});C.el('closePlayer').focus()}
 const retry=e.target.closest('[data-retry]');if(retry&&!C.viewer){retry.disabled=true;try{await C.post('/api/preview/retry',{Name:retry.dataset.retry});await C.loadClips()}catch(err){C.el('galleryMessage').textContent=err.message;retry.disabled=false}}
});
C.refreshGallery=async()=>{if(refreshBusy||document.hidden)return;refreshBusy=true;try{await C.loadClips()}catch(e){C.el('galleryMessage').textContent=e.message}finally{refreshBusy=false}};
C.refreshGallery();setInterval(C.refreshGallery,5000);

// About is available in the native app, admin and read-only viewer without API calls.
const about=C.el('aboutDialog');
function closeAbout(){about.hidden=true;document.body.style.overflow='';C.el('openAbout').focus()}
C.el('openAbout').onclick=()=>{about.hidden=false;document.body.style.overflow='hidden';C.el('closeAbout').focus()};
C.el('closeAbout').onclick=closeAbout;
about.onclick=e=>{if(e.target===about)closeAbout()};
document.addEventListener('keydown',e=>{if(about.hidden)return;if(e.key==='Escape'){e.preventDefault();closeAbout()}else if(e.key==='Tab'){e.preventDefault();C.el('closeAbout').focus()}});
