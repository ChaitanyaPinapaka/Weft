// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// tryweft.app — a custom landing at / (src/pages/index.astro) plus Starlight docs.
export default defineConfig({
  site: 'https://tryweft.app',
  integrations: [
    starlight({
      title: 'Weft',
      description: 'A local-first HTML vault that surfaces what your brain would recall now.',
      logo: { src: './src/assets/threads.svg', alt: 'Weft' },
      customCss: ['./src/styles/theme.css'],
      tableOfContents: { minHeadingLevel: 2, maxHeadingLevel: 3 },
      pagination: false,
      sidebar: [
        { label: 'Start', items: [
          { label: 'Getting started', slug: 'getting-started' },
          { label: 'Install', slug: 'install' },
          { label: 'Apps & surfaces', slug: 'apps' },
        ]},
        { label: 'Concepts', items: [{ label: 'Concepts', slug: 'concepts' }] },
        { label: 'Sync', items: [
          { label: 'Sync', slug: 'sync' },
          { label: 'Devices & recovery', slug: 'devices' },
        ]},
        { label: 'Reference', items: [
          { label: 'MCP for Claude Code', slug: 'mcp' },
          { label: 'Import', slug: 'import' },
          { label: 'CLI reference', slug: 'cli' },
        ]},
      ],
    }),
  ],
});
