import { describe, expect, it, vi } from 'vitest';
import { ApiError, createApiClient, schemas, unwrap } from './index';

const accessExample = {
  user: { id: '3f6b1c2e-6a52-4b8e-9d1e-0f2a8f5c7a10', name: 'Asha Verma', tier: 'tenant' },
  tenant: {
    id: '9a1d0c44-2b7a-4c55-8f0e-5a6a9d3b2e01',
    name: 'Springfield Public School',
    brand: { primary: '#2F5BFF' },
    modules: ['attendance', 'fee'],
  },
  permissions: ['attendance.register.view'],
  scopes: { 'attendance.register': ['class_section:12'] },
  fieldMasks: { 'student.records': ['medical'] },
  workspace: { home: 'teacher', navigation: [{ screenId: 'today', label: 'Today', parentId: null, order: 1 }] },
  mobile: { tabs: ['today', 'classes', 'inbox', 'me'], features: ['attendance.capture'] },
  approvals: { canApprove: ['exam.marks'] },
  version: 42,
};

function json(body: unknown, init: ResponseInit & { contentType?: string } = {}): Response {
  const { contentType = 'application/json', ...rest } = init;
  return new Response(JSON.stringify(body), { ...rest, headers: { 'Content-Type': contentType, ...(rest.headers ?? {}) } });
}

describe('createApiClient', () => {
  it('stamps client identity, auth token and academic year on every request', async () => {
    const seen: Request[] = [];
    const client = createApiClient({
      baseUrl: 'http://api.test/api/v1',
      client: 'ios',
      clientVersion: '1.4.0',
      getAccessToken: async () => 'tok123',
      getAcademicYear: () => 'ay-2026',
      fetch: async (input) => {
        seen.push(input as Request);
        return json({ status: 'ok', version: '0.1.0', time: '2026-11-19T10:00:00Z' });
      },
    });

    await client.GET('/healthz');

    const headers = seen[0].headers;
    expect(headers.get('X-Client')).toBe('ios');
    expect(headers.get('X-Client-Version')).toBe('1.4.0');
    expect(headers.get('Authorization')).toBe('Bearer tok123');
    expect(headers.get('X-Academic-Year')).toBe('ay-2026');
  });

  it('omits Authorization and academic year when none are available', async () => {
    const seen: Request[] = [];
    const client = createApiClient({
      baseUrl: 'http://api.test/api/v1',
      client: 'web',
      clientVersion: '0.1.0',
      fetch: async (input) => {
        seen.push(input as Request);
        return json({ status: 'ok', version: '0.1.0', time: '2026-11-19T10:00:00Z' });
      },
    });

    await client.GET('/healthz');

    expect(seen[0].headers.has('Authorization')).toBe(false);
    expect(seen[0].headers.has('X-Academic-Year')).toBe(false);
  });

  it('reports the access-payload version so the app can refetch /me/access', async () => {
    const onAccessVersion = vi.fn();
    const client = createApiClient({
      baseUrl: 'http://api.test/api/v1',
      client: 'web',
      clientVersion: '0.1.0',
      onAccessVersion,
      fetch: async () => json(accessExample, { headers: { 'X-Access-Version': '43' } }),
    });

    await client.GET('/me/access');

    expect(onAccessVersion).toHaveBeenCalledWith(43);
  });

  it('signals upgrade-required and throws a typed ApiError on 426', async () => {
    const onUpgradeRequired = vi.fn();
    const client = createApiClient({
      baseUrl: 'http://api.test/api/v1',
      client: 'android',
      clientVersion: '0.0.1',
      onUpgradeRequired,
      fetch: async () =>
        json(
          { type: 'about:blank', title: 'Upgrade Required', status: 426, code: 'client_too_old' },
          { status: 426, contentType: 'application/problem+json' },
        ),
    });

    const error = await unwrap(client.GET('/me/access')).catch((e: unknown) => e);

    expect(onUpgradeRequired).toHaveBeenCalledOnce();
    expect(error).toBeInstanceOf(ApiError);
    expect((error as ApiError).isUpgradeRequired).toBe(true);
    expect((error as ApiError).code).toBe('client_too_old');
  });

  it('maps a problem+json response to ApiError with its stable code', async () => {
    const client = createApiClient({
      baseUrl: 'http://api.test/api/v1',
      client: 'web',
      clientVersion: '0.1.0',
      fetch: async () =>
        json(
          { type: 'about:blank', title: 'Not Implemented', status: 501, code: 'not_implemented', detail: 'later' },
          { status: 501, contentType: 'application/problem+json' },
        ),
    });

    await expect(unwrap(client.GET('/me/access'))).rejects.toMatchObject({ status: 501, code: 'not_implemented' });
  });

  it('returns typed data on success', async () => {
    const client = createApiClient({
      baseUrl: 'http://api.test/api/v1',
      client: 'web',
      clientVersion: '0.1.0',
      fetch: async () => json({ status: 'ok', version: '0.1.0', time: '2026-11-19T10:00:00Z' }),
    });

    const health = await unwrap(client.GET('/healthz'));

    expect(health.status).toBe('ok');
  });
});

describe('generated Zod schemas', () => {
  it('accept the access payload example from the contract', () => {
    expect(schemas.getMyAccessResponse.safeParse(accessExample).success).toBe(true);
  });

  it('reject more than five mobile tabs', () => {
    const tooMany = { ...accessExample, mobile: { tabs: ['a', 'b', 'c', 'd', 'e', 'f'], features: [] } };
    expect(schemas.getMyAccessResponse.safeParse(tooMany).success).toBe(false);
  });

  it('reject a malformed brand colour', () => {
    const bad = { ...accessExample, tenant: { ...accessExample.tenant, brand: { primary: 'blue' } } };
    expect(schemas.getMyAccessResponse.safeParse(bad).success).toBe(false);
  });
});
