// WebGL 绿幕抠像。从 web/user/channel.html 原样提取（含 despill 与背景 cover
// 逻辑），去掉 React 依赖后作为 SDK 的独立模块 —— 抠像必须拿到 video 纹理，
// 这也正是 SDK 不能做成 iframe + postMessage 的原因。
//
// 素材绿幕为 RGB(0,177,64)。中性色（白衬衫/眼白/灰发）在 UV 色度上离绿仅 ~0.33，
// similarity 必须远小于它；greenness 保护 —— 绿色不占优的像素一律不透明；
// despill 全局生效（边缘混合像素不够绿抠不掉、但颜色带绿 → 把 g 压回 max(r,b)
// 即消绿线，素材铁律禁绿色服装所以安全）。

export const CHROMA_DEFAULTS = { key: [0, 177, 64], similarity: 0.10, smoothness: 0.08, spill: 1.0 };

// chromaFromCfg 把 /channel/:slug/config 返回的 chroma 段翻成 makeChromaKey 的入参。
export function chromaFromCfg(cfg) {
  const ck = (cfg && cfg.chroma) || {};
  const m = /^#([0-9a-f]{6})$/i.exec(ck.key_color || "");
  return {
    key: m
      ? [parseInt(m[1].slice(0, 2), 16), parseInt(m[1].slice(2, 4), 16), parseInt(m[1].slice(4, 6), 16)]
      : CHROMA_DEFAULTS.key,
    similarity: ck.similarity != null ? ck.similarity : CHROMA_DEFAULTS.similarity,
    smoothness: ck.smoothness != null ? ck.smoothness : CHROMA_DEFAULTS.smoothness,
    spill: ck.spill != null ? ck.spill : CHROMA_DEFAULTS.spill,
  };
}

// makeChromaKey 把 video 逐帧抠像画进 canvas。bgURL 为空时绿幕处透明 ——
// 这是叠在宿主页面（大屏）上的形态。返回 {stop()}；WebGL 不可用时返回 null。
export function makeChromaKey(canvas, video, bgURL, ck) {
  const gl = canvas.getContext("webgl", { premultipliedAlpha: false, alpha: true });
  if (!gl) return null;
  const vsrc = "attribute vec2 p;varying vec2 uv;void main(){uv=vec2((p.x+1.)/2.,1.-(p.y+1.)/2.);gl_Position=vec4(p,0.,1.);}";
  const fsrc = `
    precision mediump float; varying vec2 uv; uniform sampler2D tex, bgTex;
    uniform vec3 keyRGB; uniform float similarity, smoothness, spill, hasBg;
    uniform vec2 bgScale, bgOffset;
    vec2 rgb2uv(vec3 c){ return vec2(c.r*-.169+c.g*-.331+c.b*.5+.5, c.r*.5+c.g*-.419+c.b*-.081+.5); }
    void main(){
      vec4 c = texture2D(tex, uv);
      float d = distance(rgb2uv(c.rgb), rgb2uv(keyRGB));
      float alpha = smoothstep(similarity, similarity + smoothness, d);
      float greenness = c.g - max(c.r, c.b);
      alpha = mix(1., alpha, smoothstep(0., .08, greenness)); // 非绿像素保持不透明
      vec3 fg = vec3(c.r, mix(c.g, min(c.g, max(c.r, c.b)), spill), c.b);
      vec3 bg = texture2D(bgTex, uv * bgScale + bgOffset).rgb;
      gl_FragColor = mix(vec4(fg, 1.) * alpha, vec4(mix(bg, fg, alpha), 1.), hasBg);
    }`;
  const sh = (type, src) => {
    const s = gl.createShader(type);
    gl.shaderSource(s, src);
    gl.compileShader(s);
    return s;
  };
  const prog = gl.createProgram();
  gl.attachShader(prog, sh(gl.VERTEX_SHADER, vsrc));
  gl.attachShader(prog, sh(gl.FRAGMENT_SHADER, fsrc));
  gl.linkProgram(prog);
  gl.useProgram(prog);
  const buf = gl.createBuffer();
  gl.bindBuffer(gl.ARRAY_BUFFER, buf);
  gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
  const loc = gl.getAttribLocation(prog, "p");
  gl.enableVertexAttribArray(loc);
  gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);

  function makeTex(unit) {
    const t = gl.createTexture();
    gl.activeTexture(gl.TEXTURE0 + unit);
    gl.bindTexture(gl.TEXTURE_2D, t);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    return t;
  }
  makeTex(0); // video
  makeTex(1); // background image
  gl.activeTexture(gl.TEXTURE1);
  gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, 1, 1, 0, gl.RGBA, gl.UNSIGNED_BYTE, new Uint8Array([0, 0, 0, 255]));
  gl.uniform1i(gl.getUniformLocation(prog, "tex"), 0);
  gl.uniform1i(gl.getUniformLocation(prog, "bgTex"), 1);
  gl.uniform3f(gl.getUniformLocation(prog, "keyRGB"), ck.key[0] / 255, ck.key[1] / 255, ck.key[2] / 255);
  gl.uniform1f(gl.getUniformLocation(prog, "similarity"), ck.similarity);
  gl.uniform1f(gl.getUniformLocation(prog, "smoothness"), ck.smoothness);
  gl.uniform1f(gl.getUniformLocation(prog, "spill"), ck.spill);
  const uHasBg = gl.getUniformLocation(prog, "hasBg");
  const uBgScale = gl.getUniformLocation(prog, "bgScale");
  const uBgOffset = gl.getUniformLocation(prog, "bgOffset");
  gl.uniform1f(uHasBg, 0);
  gl.uniform2f(uBgScale, 1, 1);
  gl.uniform2f(uBgOffset, 0, 0);

  let bgW = 0, bgH = 0;
  if (bgURL) {
    const img = new Image();
    img.crossOrigin = "anonymous"; // 同源/开放跨域可作纹理；受限跨域则回退纯抠像
    img.onload = () => {
      try {
        gl.activeTexture(gl.TEXTURE1);
        gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, img);
        bgW = img.naturalWidth;
        bgH = img.naturalHeight;
        gl.uniform1f(uHasBg, 1);
      } catch (e) {}
    };
    img.src = bgURL;
  }
  function fitBg() { // cover：背景图等比铺满视频画幅，居中裁切
    if (!bgW || !canvas.width) return;
    const s = Math.max(canvas.width / bgW, canvas.height / bgH);
    const sx = canvas.width / (bgW * s), sy = canvas.height / (bgH * s);
    gl.uniform2f(uBgScale, sx, sy);
    gl.uniform2f(uBgOffset, (1 - sx) / 2, (1 - sy) / 2);
  }

  let raf = 0;
  function draw() {
    if (video.readyState >= 2 && video.videoWidth) {
      if (canvas.width !== video.videoWidth) {
        canvas.width = video.videoWidth;
        canvas.height = video.videoHeight;
      }
      gl.viewport(0, 0, canvas.width, canvas.height);
      fitBg();
      gl.activeTexture(gl.TEXTURE0);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, video);
      gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    }
    raf = requestAnimationFrame(draw);
  }
  raf = requestAnimationFrame(draw);
  return { stop() { cancelAnimationFrame(raf); } };
}
