import type { components } from './generated/schema';

/** RFC 9457 problem document returned by every error response. */
export type Problem = components['schemas']['Problem'];

/**
 * Error thrown by `unwrap` for any non-2xx response.
 * `code` is the stable machine code from the server (map it to a translated message, never show it raw).
 */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly problem?: Problem;

  constructor(status: number, problem?: Problem) {
    super(problem?.detail ?? problem?.title ?? `Request failed with status ${status}`);
    this.name = 'ApiError';
    this.status = status;
    this.code = problem?.code ?? 'unknown_error';
    this.problem = problem;
  }

  /** True when the server says this client is too old and must be updated. */
  get isUpgradeRequired(): boolean {
    return this.status === 426;
  }

  /** True when the session is missing or expired. */
  get isUnauthorized(): boolean {
    return this.status === 401;
  }

  /** True when the caller is signed in but not allowed (permission, scope or module). */
  get isForbidden(): boolean {
    return this.status === 403;
  }
}

function isProblem(value: unknown): value is Problem {
  return typeof value === 'object' && value !== null && 'code' in value && 'status' in value;
}

/**
 * Turns an openapi-fetch result into its data, or throws ApiError.
 * Use it so screens deal with one success path and one error type.
 */
export async function unwrap<T>(
  result: Promise<{ data?: T; error?: unknown; response: Response }>,
): Promise<T> {
  const { data, error, response } = await result;
  if (!response.ok || data === undefined) {
    throw new ApiError(response.status, isProblem(error) ? error : undefined);
  }
  return data;
}
