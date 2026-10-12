// SDK 构建：esbuild 打成单文件，直接由 cored 的静态目录托管。
//
//   node web/sdk/build.mjs          # 生产（压缩）
//   node web/sdk/build.mjs --watch  # 开发
//
// 仓里不引入前端构建体系 —— esbuild 用 npx 拉，产物入库，接入方
// <script src="…/sdk/mynah.min.js"> 一行就够。
//
// 产物落在 web/user/sdk/ 而不是 web/sdk/dist/：cored 的 FileServer 只托管
// webDir（默认 web/user），落在这里就不用加路由、加 flag，现有部署和 compose
// 的 bind-mount 原样生效。
import { build, context } from "esbuild";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const outDir = resolve(here, "../user/sdk");
const watch = process.argv.includes("--watch");

// IIFE 下 globalName 拿到的是模块命名空间对象。把 Mynah 类本身提上去
// （new Mynah(...) 直接可用），抠像工具挂成它的属性，命名空间整体留在
// window.MynahSDK 备用。
const footer = `
window.Mynah = MynahSDK.Mynah;
window.Mynah.makeChromaKey = MynahSDK.makeChromaKey;
window.Mynah.chromaFromCfg = MynahSDK.chromaFromCfg;
`.trim();

const common = {
  entryPoints: [resolve(here, "src/index.js")],
  bundle: true,
  format: "iife",
  globalName: "MynahSDK",
  footer: { js: footer },
  target: ["es2020"],
  charset: "utf8",
  banner: { js: "/* Mynah SDK — Apache-2.0 — https://github.com/honwee/mynah */" },
};

// mynah.* is the name; personalive.* is kept as a byte-identical legacy alias so
// existing <script src="…/sdk/personalive.js"> embeds do not break.
const outputs = [
  { ...common, outfile: resolve(outDir, "mynah.js"), minify: false, sourcemap: true },
  { ...common, outfile: resolve(outDir, "mynah.min.js"), minify: true },
  { ...common, outfile: resolve(outDir, "personalive.js"), minify: false, sourcemap: true },
  { ...common, outfile: resolve(outDir, "personalive.min.js"), minify: true },
];

if (watch) {
  const ctxs = await Promise.all(outputs.map((o) => context(o)));
  await Promise.all(ctxs.map((c) => c.watch()));
  console.log("watching web/sdk/src …");
} else {
  await Promise.all(outputs.map((o) => build(o)));
  console.log("built web/user/sdk/mynah{,.min}.js (+ personalive.* legacy aliases)");
}
