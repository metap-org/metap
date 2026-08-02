export type TransitionAvailability = {
  action: string;
  available: boolean;
  reason?: string;
};

export type RecordCapabilities = {
  writableFields: string[];
  canUpdate: boolean;
  transitions: TransitionAvailability[];
};
