import type { Env as AppEnv } from '../src/types';

declare global {
  namespace Cloudflare {
    interface Env extends AppEnv {
      TEST_MIGRATIONS: { name: string; queries: string[] }[];
    }
  }
}
