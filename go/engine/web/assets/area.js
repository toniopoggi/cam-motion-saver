'use strict';
(()=>{
 const A=window.CFN,$=A.el,stage=$('frameStage'),canvas=$('areaCanvas'),box=$('areaRect'),img=$('liveFrame');
 let area={X:0,Y:0,Width:1,Height:1},saved={...area},editing=false,fresh=false,drag=null,objectUrl=null,visible=false,busy=false;
 const clamp=(v,a,b)=>Math.max(a,Math.min(b,v));
 function show(){box.style.left=(area.X*100)+'%';box.style.top=(area.Y*100)+'%';box.style.width=(area.Width*100)+'%';box.style.height=(area.Height*100)+'%'}
 function edit(on){editing=on;canvas.classList.toggle('editing',on);$('defineArea').hidden=on;['saveArea','resetArea','cancelArea'].forEach(id=>$(id).hidden=!on);box.querySelectorAll('[data-handle]').forEach(h=>h.hidden=!on)}
 function point(e){const b=stage.getBoundingClientRect();return {x:clamp((e.clientX-b.left)/b.width,0,1),y:clamp((e.clientY-b.top)/b.height,0,1)}}
 canvas.addEventListener('pointerdown',e=>{if(!editing||!stage.classList.contains('has-frame'))return;e.preventDefault();const p=point(e),handle=e.target.dataset.handle||'',kind=handle||((e.target===box&&!fresh)?'move':'draw');drag={kind,start:p,rect:{...area}};fresh=false;canvas.setPointerCapture(e.pointerId)});
 canvas.addEventListener('pointermove',e=>{if(!drag)return;e.preventDefault();const p=point(e),a=drag.rect,dx=p.x-drag.start.x,dy=p.y-drag.start.y,min=.02;
  if(drag.kind==='draw'){area={X:Math.min(p.x,drag.start.x),Y:Math.min(p.y,drag.start.y),Width:Math.abs(p.x-drag.start.x),Height:Math.abs(p.y-drag.start.y)}}
  else if(drag.kind==='move'){area={...a,X:clamp(a.X+dx,0,1-a.Width),Y:clamp(a.Y+dy,0,1-a.Height)}}
  else{let l=a.X,t=a.Y,r=a.X+a.Width,b=a.Y+a.Height,k=drag.kind;if(k.includes('w'))l=clamp(a.X+dx,0,r-min);if(k.includes('e'))r=clamp(a.X+a.Width+dx,l+min,1);if(k.includes('n'))t=clamp(a.Y+dy,0,b-min);if(k.includes('s'))b=clamp(a.Y+a.Height+dy,t+min,1);area={X:l,Y:t,Width:r-l,Height:b-t}}
  show();
 });
 const end=()=>{if(!drag)return;drag=null;if(area.Width<.02||area.Height<.02){area={...saved};$('areaMessage').textContent='Draw a larger area.'}show()};canvas.addEventListener('pointerup',end);canvas.addEventListener('pointercancel',end);
 $('defineArea').onclick=()=>{if(!stage.classList.contains('has-frame')){$('areaMessage').textContent='The camera preview must load before drawing an area.';return}edit(true);fresh=true;$('areaMessage').textContent='Drag on the image to draw. Drag a handle to resize.'};
 $('cancelArea').onclick=()=>{area={...saved};show();edit(false);$('areaMessage').textContent='Area unchanged.'};
 $('resetArea').onclick=()=>{area={X:0,Y:0,Width:1,Height:1};show();$('areaMessage').textContent='Whole image selected. Tap Save area.'};
 $('saveArea').onclick=async()=>{if(area.Width<.02||area.Height<.02){$('areaMessage').textContent='Draw a larger area.';return}try{const r=await A.post('/api/recording/settings',{MotionArea:area});saved={...r.MotionArea};area={...saved};show();edit(false);$('areaMessage').textContent='Motion area saved.'}catch(e){$('areaMessage').textContent=e.message}};
 A.json('/api/recording/settings').then(c=>{if(c.MotionArea){area={...c.MotionArea};saved={...area};show()}}).catch(e=>{$('areaMessage').textContent=e.message});
 async function frame(){if(!visible||document.hidden||busy)return;busy=true;try{const r=await fetch('/api/live/frame',{cache:'no-store'});if(r.status===204){$('liveMessage').textContent=r.headers?.get('X-Preview-Status')||'Waiting for camera preview…';return}if(!r.ok)throw Error('Live preview unavailable ('+r.status+').');const blob=await r.blob(),next=URL.createObjectURL(blob),previous=objectUrl;objectUrl=next;img.onload=()=>{img.hidden=false;stage.classList.add('has-frame');$('liveWaiting').hidden=true;$('liveMessage').textContent='Live preview · updated about once per second.';if(previous)URL.revokeObjectURL(previous)};img.onerror=()=>{$('liveMessage').textContent='The browser could not display the camera preview.';if(objectUrl===next)objectUrl=null;URL.revokeObjectURL(next);if(previous)URL.revokeObjectURL(previous)};img.src=next}catch(e){$('liveMessage').textContent=e.message||'Live preview unavailable.'}finally{busy=false}}
 if('IntersectionObserver' in window){new IntersectionObserver(entries=>{visible=entries.some(e=>e.isIntersecting);if(visible)frame()},{rootMargin:'100px'}).observe(stage)}else visible=true;
 document.addEventListener('visibilitychange',()=>{if(!document.hidden&&visible)frame()});setInterval(frame,1000);show();edit(false);
})();
