-- A second factor that can be guessed is not one (audit 07): wrong codes are counted per identity,
-- whatever route they arrive on (sign-in, disabling MFA, regenerating recovery codes, deleting the
-- workspace), and too many lock the factor for a while.
ALTER TABLE metadata.mfa_factors
  ADD COLUMN failed_attempts integer NOT NULL DEFAULT 0,
  ADD COLUMN locked_until timestamptz;
