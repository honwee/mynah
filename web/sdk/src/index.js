// SDK 的入口：同时导出类和自定义元素，并在 IIFE 构建里挂到 window.Mynah，
// 这样 <script src> 一行接入和 ESM import 两种用法都成立。
export { Mynah, Mynah as default } from "./mynah.js";
export { chromaFromCfg, makeChromaKey, CHROMA_DEFAULTS } from "./chroma.js";
export { MynahAvatar, MynahAbility } from "./element.js";
