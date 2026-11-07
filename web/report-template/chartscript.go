package reporttemplate

// visualChartScript is the template's chart library plus a fixed dispatcher.
// It never contains report data; data lives in the application/json block.
const visualChartScript = `(function(){
  "use strict";
  var NS="http://www.w3.org/2000/svg";
  function mk(t,a,p){var e=document.createElementNS(NS,t);for(var k in a){if(a.hasOwnProperty(k))e.setAttribute(k,a[k]);}if(p)p.appendChild(e);return e;}
  function host(id){return document.getElementById(id);}
  function cssv(name){return getComputedStyle(document.documentElement).getPropertyValue(name).trim();}
  function resolveColor(c){
    return (typeof c==="string"&&c.indexOf("var(")===0)?cssv(c.slice(4,-1)):c;
  }
  function svgIn(id,w,h){var s=mk("svg",{viewBox:"0 0 "+w+" "+h,role:"img"},host(id));return s;}
  /* 画布宽度随视口收窄：手机上改 viewBox 宽度而不是放大字号 */
  function chartW(base){
    var vw = window.innerWidth || 1024;
    return Math.max(360, Math.min(base, Math.round((vw - 62) * 1.27)));
  }

  function niceMax(v){var p=Math.pow(10,Math.floor(Math.log10(v)));var n=Math.ceil(v/p*2)/2*p;return n;}
  function txt(s,x,y,cls,content,anchor){var t=mk("text",{x:x,y:y,class:cls},s);if(anchor)t.setAttribute("text-anchor",anchor);t.textContent=content;return t;}

  /* ========== 1. 竖条图 ==========
     用法：chartBar('容器id', {data:[{label,value}], unit:'单位', highlight:'last'})
  */
  function chartBar(id,cfg){
    var data=cfg.data, W=chartW(760), H=cfg.height||320, L=58, R=24, T=32, B=54;
    var s=svgIn(id,W,H);
    var max=cfg.max||niceMax(Math.max.apply(null,data.map(function(d){return d.value;})));
    var iw=W-L-R, ih=H-T-B, step=iw/data.length, bw=step*0.46;
    var y=function(v){return T+ih-(v/max)*ih;};
    var ticks=5;
    for(var i=0;i<=ticks;i++){
      var v=max/ticks*i;
      mk("line",{x1:L,y1:y(v),x2:W-R,y2:y(v),class:"gridline"},s);
      txt(s,L-10,y(v)+4,"lbl",Math.round(v*10)/10,"end");
    }
    mk("line",{x1:L,y1:T+ih,x2:W-R,y2:T+ih,class:"axis"},s);
    if(cfg.unit)txt(s,L,12,"lbl",cfg.unit);
    data.forEach(function(d,i){
      var cx=L+step*i+step/2, h=(d.value/max)*ih;
      var hi=(cfg.highlight==="last"&&i===data.length-1)||cfg.highlight===i;
      mk("rect",{x:cx-bw/2,y:T+ih-h,width:bw,height:Math.max(h,1),rx:2,
        fill:hi?cssv("--accent"):cssv("--deep2")},s);
      txt(s,cx,T+ih-h-9,"val",d.value,"middle");
      txt(s,cx,T+ih+22,"lbl",d.label,"middle");
    });
  }

  /* ========== 2. 横向条图 ==========
     用法：chartHBar('容器id', {data:[{label,value,note}], unit:'单位'})
  */
  function chartHBar(id,cfg){
    var data=cfg.data, W=chartW(760), H=30+data.length*42+46, L=cfg.labelWidth||200, R=70, T=28;
    var s=svgIn(id,W,H);
    var max=cfg.max||niceMax(Math.max.apply(null,data.map(function(d){return d.value;})));
    var x=function(v){return L+(v/max)*(W-L-R);};
    if(cfg.unit)txt(s,L,12,"lbl",cfg.unit);
    for(var i=0;i<=4;i++){
      var v=max/4*i;
      mk("line",{x1:x(v),y1:T-8,x2:x(v),y2:T+data.length*42-8,class:"gridline"},s);
      txt(s,x(v),T+data.length*42+20,"lbl",Math.round(v),"middle");
    }
    data.forEach(function(d,i){
      var yy=T+i*42;
      txt(s,L-14,yy+16,"lbl-d",d.label,"end");
      mk("rect",{x:L,y:yy+3,width:Math.max(x(d.value)-L,2),height:20,rx:2,
        fill:i===0?cssv("--accent"):cssv("--deep2")},s);
      txt(s,x(d.value)+9,yy+19,"val",d.note||d.value);
    });
  }

  /* ========== 3. 四象限 ==========
     用法：chartQuadrant('容器id', {points:[{x:0~1,y:0~1,label,sub,color}], xLabel, yLabel, corners})
  */
  function chartQuadrant(id,cfg){
    var W=chartW(760),H=440,L=82,R=34,T=44,B=64;
    var s=svgIn(id,W,H);
    var iw=W-L-R, ih=H-T-B, cx=L+iw/2, cy=T+ih/2;
    mk("rect",{x:L,y:T,width:iw/2,height:ih/2,fill:"#FBF3EE"},s);
    mk("rect",{x:cx,y:T,width:iw/2,height:ih/2,fill:"#EFF7E2"},s);
    mk("rect",{x:L,y:cy,width:iw/2,height:ih/2,fill:"#F6F4EC"},s);
    mk("rect",{x:cx,y:cy,width:iw/2,height:ih/2,fill:"#FAF8F2"},s);
    mk("line",{x1:L,y1:cy,x2:W-R,y2:cy,class:"axis"},s);
    mk("line",{x1:cx,y1:T,x2:cx,y2:T+ih,class:"axis"},s);
    var c=cfg.corners||{};
    if(c.tl)txt(s,L+10,T+20,"lbl",c.tl).setAttribute("fill","#A8663E");
    if(c.tr){var t1=txt(s,W-R-10,T+20,"lbl",c.tr,"end");t1.setAttribute("fill","#4C7A2E");}
    if(c.bl)txt(s,L+10,T+ih-10,"lbl",c.bl);
    if(c.br)txt(s,W-R-10,T+ih-10,"lbl",c.br,"end");
    txt(s,L-10,T+4,"lbl","高","end"); txt(s,L-10,T+ih,"lbl","低","end");
    txt(s,L,H-40,"lbl","低","middle"); txt(s,cx,H-40,"lbl",(cfg.xLabel||"X 轴 →"),"middle");
    txt(s,W-R,H-40,"lbl","高","middle");
    var yl=mk("text",{x:0,y:0,"text-anchor":"middle",class:"lbl",
      transform:"translate(26,"+(T+ih/2)+") rotate(-90)"},s);
    yl.textContent=(cfg.yLabel||"Y 轴 →");
    (cfg.points||[]).forEach(function(p){
      var px=L+p.x*iw, py=T+ih-p.y*ih;
      mk("circle",{cx:px,cy:py,r:p.r||9,fill:resolveColor(p.color||"var(--deep2)")},s);
      txt(s,px,py-(p.r||9)-9,"lbl-d",p.label,"middle");
      if(p.sub)txt(s,px,py+(p.r||9)+16,"lbl",p.sub,"middle").setAttribute("style","font-size:10.5px;fill:#7A8188");
    });
  }

  /* ========== 4. 甜甜圈 ==========
     用法：chartDonut('容器id', {data:[{label,value,color}], center:{big,small}})
  */
  function chartDonut(id,cfg){
    var data=cfg.data, W=420,H=280,cx=150,cy=140,r=94,sw=34;
    var s=svgIn(id,W,H);
    var total=data.reduce(function(a,b){return a+b.value;},0);
    var circ=2*Math.PI*r, off=0;
    data.forEach(function(d){
      var len=circ*d.value/total;
      mk("circle",{cx:cx,cy:cy,r:r,fill:"none",stroke:d.color,"stroke-width":sw,
        "stroke-dasharray":(len-2)+" "+(circ-len+2),
        "stroke-dashoffset":-off,transform:"rotate(-90 "+cx+" "+cy+")"},s);
      off+=len;
    });
    var c=cfg.center||{};
    if(c.big){
      var big=mk("text",{x:cx,y:cy+2,"text-anchor":"middle"},s);
      big.setAttribute("style","font-family:"+cssv("--mono")+";font-size:26px;fill:"+cssv("--deep")+";font-weight:700");
      big.textContent=c.big;
    }
    if(c.small){
      var sm=mk("text",{x:cx,y:cy+22,"text-anchor":"middle"},s);
      sm.setAttribute("style","font-size:11px;fill:"+cssv("--muted"));
      sm.textContent=c.small;
    }
    data.forEach(function(d,i){
      var ly=44+i*36;
      mk("rect",{x:272,y:ly-9,width:10,height:10,rx:2,fill:d.color},s);
      var lb=mk("text",{x:290,y:ly},s);
      lb.setAttribute("style","font-size:12px;fill:"+cssv("--ink2"));
      lb.textContent=d.label;
      var pv=mk("text",{x:290,y:ly+15},s);
      pv.setAttribute("style","font-size:11px;fill:"+cssv("--muted")+";font-family:"+cssv("--mono"));
      pv.textContent=d.value+"%";
    });
  }

  /* ========== 5. 折线图 ==========
     单序列：chartLine('c5', {data:[{label,value}], unit:'单位'})
     多序列：chartLine('c6', {labels:[...], series:[{name,values,color,dashed}]})
  */
  function chartLine(id,cfg){
    var series = cfg.series;
    if(!series){
      series = [{
        name: cfg.name || "",
        values: (cfg.data||[]).map(function(d){ return d.value; }),
        color: cfg.color || "var(--deep2)"
      }];
    }
    var labels = cfg.labels || (cfg.data||[]).map(function(d){ return d.label; });
    var W=chartW(760), H=cfg.height||330, L=58, R=24, T=cfg.series?44:34, B=54;
    var s=svgIn(id,W,H);
    var flat=[]; series.forEach(function(se){ flat=flat.concat(se.values); });
    var max=cfg.max||niceMax(Math.max.apply(null,flat));
    var iw=W-L-R, ih=H-T-B, n=labels.length;
    var y=function(v){ return T+ih-(v/max)*ih; };
    var x=function(i){ return n<=1 ? L+iw/2 : L+(iw*i/(n-1)); };

    var ticks=5;
    for(var t=0;t<=ticks;t++){
      var v=max/ticks*t;
      mk("line",{x1:L,y1:y(v),x2:W-R,y2:y(v),class:"gridline"},s);
      txt(s,L-10,y(v)+4,"lbl",Math.round(v*10)/10,"end");
    }
    mk("line",{x1:L,y1:T+ih,x2:W-R,y2:T+ih,class:"axis"},s);
    if(cfg.unit) txt(s,L,12,"lbl",cfg.unit);
    for(var i=0;i<n;i++) txt(s,x(i),T+ih+22,"lbl",labels[i],"middle");

    var gid="lg-"+id, defs=mk("defs",{},s);
    var grad=mk("linearGradient",{id:gid,x1:"0",y1:"0",x2:"0",y2:"1"},defs);
    mk("stop",{offset:"0%","stop-color":"rgba(23,64,47,.20)"},grad);
    mk("stop",{offset:"100%","stop-color":"rgba(23,64,47,0)"},grad);

    series.forEach(function(se){
      var col=resolveColor(se.color||"var(--deep2)");
      var pts=se.values.map(function(v,i){ return [x(i), y(v)]; });
      var d=pts.map(function(pt,i){ return (i?"L":"M")+pt[0]+" "+pt[1]; }).join(" ");

      if(series.length===1 && cfg.area!==false && se.fill!==false){
        var base=y(0);
        var area=d+" L"+pts[pts.length-1][0]+" "+base+" L"+pts[0][0]+" "+base+" Z";
        mk("path",{d:area,fill:"url(#"+gid+")",stroke:"none"},s);
      }
      mk("path",{d:d,fill:"none",stroke:col,"stroke-width":2.5,
        "stroke-linecap":"round","stroke-linejoin":"round",
        "stroke-dasharray": se.dashed ? "7 5" : "none"},s);

      pts.forEach(function(pt,i){
        var last=i===pts.length-1;
        mk("circle",{cx:pt[0],cy:pt[1],r:last?5:3.2,
          fill:last?col:cssv("--card"),stroke:col,"stroke-width":2},s);
      });

      var showAll = series.length===1 && n<=8;
      pts.forEach(function(pt,i){
        if(!showAll && i!==pts.length-1) return;
        txt(s,pt[0],pt[1]-(i===pts.length-1?13:11),"val",se.values[i],"middle");
      });
    });

    if(series.length>1){
      var lx=L, ly=T-16;
      series.forEach(function(se){
        var col=resolveColor(se.color||"var(--deep2)");
        mk("line",{x1:lx,y1:ly-4,x2:lx+18,y2:ly-4,stroke:col,"stroke-width":2.5,
          "stroke-dasharray": se.dashed ? "6 4" : "none"},s);
        var t=txt(s,lx+24,ly,"",se.name);
        t.setAttribute("style","font-size:11.5px;fill:"+cssv("--muted"));
        lx += 24 + se.name.length*12 + 28;
      });
    }
  }

  /* ========== 6. 计算器 ==========
     cfg.inputs : [{key,label,min,max,step,value,unit,fmt}]
     cfg.outputs: [{key,label,big,kind}]   kind: money | pct | months | int | text
     cfg.compute: (v) => {key:value,...}
  */
  function calculator(sel,cfg){
    var wrap=document.querySelector(sel);
    if(!wrap)return;
    var inBox=wrap.querySelector(".calc-grid > div:first-child")||wrap.querySelector("#demo-calc-in");
    var outBox=wrap.querySelector(".calc-out");
    if(!inBox||!outBox)return;

    cfg.inputs.forEach(function(it){
      var row=document.createElement("div");
      row.className="calc-row";
      row.innerHTML='<label>'+it.label+' <b></b></label>'+
        '<input type="range" min="'+it.min+'" max="'+it.max+'" step="'+it.step+'" value="'+it.value+'" data-key="'+it.key+'">';
      inBox.appendChild(row);
    });
    cfg.outputs.forEach(function(o){
      var d=document.createElement("div");
      d.className="o"+(o.big?" big":"");
      d.innerHTML='<span>'+o.label+'</span><b data-out="'+o.key+'">—</b>';
      outBox.appendChild(d);
    });

    var inputs=[].slice.call(inBox.querySelectorAll("input[type=range]"));
    function fmtInput(it,v){
      if(it.fmt==="wan")return (v/10000).toFixed(1)+" 万元";
      if(it.fmt==="pct")return Math.round(v)+"%";
      return v+" "+(it.unit||"");
    }
    function fmtOut(kind,v){
      if(kind==="money")return (v/10000).toFixed(1)+" 万";
      if(kind==="pct")return (v*100).toFixed(1)+"%";
      if(kind==="months")return v>0?(v<120?v.toFixed(0)+" 个月":"＞10 年"):"无法回收";
      return String(Math.round(v));
    }
    function run(){
      var v={};
      inputs.forEach(function(el){
        var it=cfg.inputs.filter(function(x){return x.key===el.dataset.key;})[0];
        v[it.key]=+el.value;
        var lab=el.parentNode.querySelector("label b");
        if(lab)lab.textContent=fmtInput(it,+el.value);
      });
      var res=cfg.compute(v);
      cfg.outputs.forEach(function(o){
        var el=outBox.querySelector('[data-out="'+o.key+'"]');
        if(!el)return;
        var val=res[o.key];
        el.textContent=(o.kind==="text")?val:fmtOut(o.kind,val);
        if(o.kind==="money"&&o.big)el.style.color=(val>=0?"var(--accent)":"#F08A5D");
      });
    }
    inputs.forEach(function(el){el.addEventListener("input",run);});
    run();
  }
  var d=document.getElementById("zeus-chart-data");
  if(d){var items;try{items=JSON.parse(d.textContent)}catch(e){return}
    var fns={line:chartLine,hbar:chartHBar,donut:chartDonut,quadrant:chartQuadrant};
    for(var i=0;i<items.length;i++){var it=items[i],fn=fns[it.renderType]||chartBar;if(fn)fn("c"+i,it.config)}
  }
})();`

// visualChartScriptHash is pinned; TestVisualChartScriptHashIsPinned fails if
// the script bytes change without updating this value.
const visualChartScriptHash = "9xBUbcvmxtcTSMjOWWE1ZxYfLQETUGT4A81QDQq9g4c="
