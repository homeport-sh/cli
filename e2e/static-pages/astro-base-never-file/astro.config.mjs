import { defineConfig } from 'astro/config';
import node from '@astrojs/node';

export default defineConfig({
  output: 'server',
  adapter: node({ mode: 'standalone' }),
  base: '/docs',
  trailingSlash: 'never',
  build: { format: 'file', inlineStylesheets: 'never' },
});
