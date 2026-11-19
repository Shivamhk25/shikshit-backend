import createClient, { type Middleware } from 'openapi-fetch';
import type { paths } from './generated/schema';

/** Which client is calling; the server uses it to decide which screens the caller can render. */
export type ClientName = 'web' | 'ios' | 'android';

export interface ApiClientOptions {
  /** Defaults to `/api/v1` (proxied by Vite in development). */
  baseUrl?: string;
  client: ClientName;
  /** Semantic version of the web build or the mobile app. */
  clientVersion: string;
  /** Returns the current access token, if signed in. */
  getAccessToken?: () => string | undefined | Promise<string | undefined>;
  /** Returns the selected academic year id; sent as `X-Academic-Year`. */
  getAcademicYear?: () => string | undefined;
  /** Called when the server reports a different access-payload version; refetch `/me/access`. */
  onAccessVersion?: (version: number) => void;
  /** Called on HTTP 426: this client is below the minimum supported version. */
  onUpgradeRequired?: () => void;
  /** Override for tests or React Native polyfills. */
  fetch?: typeof fetch;
}

export const ACCESS_VERSION_HEADER = 'X-Access-Version';

/**
 * Creates the typed API client shared by web and mobile.
 * It stamps every request with client identity, auth and academic year, and surfaces
 * the access-version and upgrade-required signals defined in docs/plan/08-web-mobile-sync.md.
 */
export function createApiClient(options: ApiClientOptions) {
  const client = createClient<paths>({
    baseUrl: options.baseUrl ?? '/api/v1',
    fetch: options.fetch,
  });

  const middleware: Middleware = {
    async onRequest({ request }) {
      request.headers.set('X-Client', options.client);
      request.headers.set('X-Client-Version', options.clientVersion);
      const token = await options.getAccessToken?.();
      if (token) request.headers.set('Authorization', `Bearer ${token}`);
      const year = options.getAcademicYear?.();
      if (year) request.headers.set('X-Academic-Year', year);
      return request;
    },
    onResponse({ response }) {
      const raw = response.headers.get(ACCESS_VERSION_HEADER);
      if (raw !== null) {
        const version = Number.parseInt(raw, 10);
        if (Number.isFinite(version)) options.onAccessVersion?.(version);
      }
      if (response.status === 426) options.onUpgradeRequired?.();
      return undefined;
    },
  };
  client.use(middleware);
  return client;
}

export type ApiClient = ReturnType<typeof createApiClient>;
