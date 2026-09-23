import type { ScoreSnapshot } from './types';

/**
 * Decodifica ScoreRow.inputs_snapshot (JSON en base64, ADR-0004) a un objeto
 * parcial tipado. El snapshot es opaco por contrato; las claves que la UI usa
 * están en ScoreSnapshot, el resto se ignora. Null si no hay snapshot o el
 * base64/JSON es inválido (no debe romper la vista).
 */
export function decodeScoreSnapshot(
  snapshot: string | null | undefined,
): Partial<ScoreSnapshot> | null {
  if (!snapshot) return null;
  try {
    return JSON.parse(atob(snapshot)) as Partial<ScoreSnapshot>;
  } catch {
    return null;
  }
}