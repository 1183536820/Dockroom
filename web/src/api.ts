import type {components} from './contract';
export type Contract = components['schemas'];
export const connectedView = location.pathname === '/connected/';
export class ApiError extends Error {
  constructor(readonly status: number, readonly code: string, message: string) { super(message); }
}
export async function call<T>(path: string, opts: {method?: string; body?: unknown; csrf?: string; signal?: AbortSignal; idempotency?: string} = {}): Promise<T> {
  const res = await fetch(`${connectedView?'/connected':''}/api/v1${path}`, {
    method: opts.method ?? 'GET', signal: opts.signal, cache: 'no-store',
    headers: {...(opts.body === undefined ? {} : {'Content-Type': 'application/json'}), ...(opts.csrf ? {'X-CSRF-Token': opts.csrf} : {}), ...(opts.idempotency ? {'Idempotency-Key': opts.idempotency} : {})},
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
  });
  if (!res.ok) {
    const error = await res.json().catch(() => ({})) as Partial<Contract['Failure']>;
    throw new ApiError(res.status, error.code ?? 'REQUEST_FAILED', error.message ?? `请求失败（${res.status}）`);
  }
  return res.status === 204 ? undefined as T : res.json() as Promise<T>;
}
