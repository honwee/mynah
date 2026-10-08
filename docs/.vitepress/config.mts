import { defineConfig } from 'vitepress'

// Mynah docs — bilingual (en root, zh under /zh/). Built by .github/workflows/docs.yml
// and published to GitHub Pages at https://honwee.github.io/mynah/ (custom domain later).
export default defineConfig({
  title: 'Mynah',
  description: 'Open-source digital humans, ready to deploy.',
  base: '/mynah/',
  lastUpdated: true,
  ignoreDeadLinks: true,
  head: [['link', { rel: 'icon', href: '/mynah/favicon.png' }]],
  themeConfig: { logo: '/mynah-mark.png', siteTitle: 'Mynah' },
  locales: {
    root: {
      label: 'English', lang: 'en-US',
      themeConfig: {
        nav: [
          { text: 'Guide', link: '/guide/quickstart' },
          { text: 'Console', link: '/guide/console' },
          { text: 'API', link: '/api/admin-api' },
          { text: 'GitHub', link: 'https://github.com/honwee/mynah' },
        ],
        sidebar: {
          '/guide/': [
            { text: 'Getting started', items: [
              { text: 'Quickstart (15 min, no GPU)', link: '/guide/quickstart' },
              { text: 'Hardware & engines', link: '/guide/hardware' },
              { text: 'Deploy with Docker Compose', link: '/guide/deploy' },
              { text: 'Cloud images (AutoDL / UCloud)', link: '/guide/cloud-images' },
            ]},
            { text: 'Using the console', items: [
              { text: 'Console tour', link: '/guide/console' },
              { text: 'Avatars & voices', link: '/guide/avatars' },
              { text: 'Knowledge base', link: '/guide/knowledge' },
              { text: 'Publish a channel', link: '/guide/channels' },
              { text: 'TTS tiers (EdgeTTS → Qwen3-TTS → cloud)', link: '/guide/tts' },
            ]},
            { text: 'Integrate', items: [
              { text: 'Embed the visitor widget / SDK', link: '/guide/embed' },
              { text: 'Agents (DeepSeek Harness plugin)', link: '/guide/agents' },
              { text: 'Admin API', link: '/api/admin-api' },
              { text: 'TTS voice contract', link: '/api/tts-voice-contract' },
            ]},
            { text: 'Reference', items: [
              { text: 'Engine pool & capacity', link: '/design/avatar-engine-pool' },
              { text: 'Avatar bake pipeline', link: '/design/avatar-bake-pipeline' },
              { text: 'Compliance', link: '/compliance' },
              { text: 'FAQ', link: '/guide/faq' },
            ]},
          ],
        },
      },
    },
    zh: {
      label: '简体中文', lang: 'zh-CN', link: '/zh/',
      themeConfig: {
        nav: [
          { text: '指南', link: '/zh/guide/quickstart' },
          { text: '控制台', link: '/zh/guide/console' },
          { text: 'API', link: '/api/admin-api' },
          { text: 'GitHub', link: 'https://github.com/honwee/mynah' },
          { text: 'GitCode 镜像', link: 'https://gitcode.com/honwee/mynah' },
        ],
        sidebar: {
          '/zh/guide/': [
            { text: '开始', items: [
              { text: '15 分钟跑通（无需显卡）', link: '/zh/guide/quickstart' },
              { text: '硬件要求与引擎选择', link: '/zh/guide/hardware' },
              { text: 'Docker Compose 部署', link: '/zh/guide/deploy' },
              { text: '云镜像（AutoDL / UCloud）', link: '/zh/guide/cloud-images' },
            ]},
            { text: '控制台', items: [
              { text: '控制台总览', link: '/zh/guide/console' },
              { text: '形象与声音', link: '/zh/guide/avatars' },
              { text: '知识库', link: '/zh/guide/knowledge' },
              { text: '发布频道', link: '/zh/guide/channels' },
              { text: 'TTS 三档（EdgeTTS → Qwen3-TTS → 云端）', link: '/zh/guide/tts' },
            ]},
            { text: '接入', items: [
              { text: '嵌入访客组件 / SDK', link: '/zh/guide/embed' },
              { text: 'Agent 接入（DeepSeek Harness 插件）', link: '/zh/guide/agents' },
              { text: '管理 API', link: '/api/admin-api' },
              { text: 'TTS 音色契约', link: '/api/tts-voice-contract' },
            ]},
            { text: '参考', items: [
              { text: '引擎池与容量', link: '/design/avatar-engine-pool' },
              { text: '形象烘焙管线', link: '/design/avatar-bake-pipeline' },
              { text: '合规', link: '/compliance' },
              { text: '常见问题', link: '/zh/guide/faq' },
            ]},
          ],
        },
      },
    },
  },
})
