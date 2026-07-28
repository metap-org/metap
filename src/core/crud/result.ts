export type ServiceResult<T> =
  | {
      ok: true;
      data: T;
      page?: unknown;
    }
  | {
      ok: false;
      status: number;
      error: string;
    };
