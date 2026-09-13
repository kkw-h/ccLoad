(function initModelReasoningEfforts(root) {
  'use strict';

  const REASONING_EFFORT_ORDER = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];
  const REASONING_EFFORT_SET = new Set(REASONING_EFFORT_ORDER);
  const MAX_OVERRIDES = 500;
  const MAX_MODEL_NAME_LENGTH = 255;

  function normalizeOverrides(value) {
    if (!value || typeof value !== 'object' || Array.isArray(value)) {
      throw new TypeError('overrides must be an object');
    }

    const entries = Object.entries(value);
    if (entries.length > MAX_OVERRIDES) {
      throw new TypeError(`overrides must contain at most ${MAX_OVERRIDES} models`);
    }

    const normalized = {};
    const seenModels = new Set();
    for (const [rawModel, rawEfforts] of entries) {
      const model = String(rawModel).trim().toLowerCase();
      if (!model || Array.from(model).length > MAX_MODEL_NAME_LENGTH) {
        throw new TypeError('invalid model name');
      }
      if (seenModels.has(model)) {
        throw new TypeError(`duplicate model after normalization: ${model}`);
      }
      seenModels.add(model);
      if (!Array.isArray(rawEfforts)) {
        throw new TypeError(`efforts for ${model} must be an array`);
      }

      const selected = new Set();
      for (const rawEffort of rawEfforts) {
        if (typeof rawEffort !== 'string') {
          throw new TypeError(`efforts for ${model} must be strings`);
        }
        const effort = rawEffort.trim().toLowerCase();
        if (!REASONING_EFFORT_SET.has(effort)) {
          throw new TypeError(`unknown effort: ${effort}`);
        }
        selected.add(effort);
      }
      normalized[model] = REASONING_EFFORT_ORDER.filter((effort) => selected.has(effort));
    }

    return Object.fromEntries(
      Object.entries(normalized).sort(([left], [right]) => left.localeCompare(right))
    );
  }

  function upsertOverride(value, model, efforts) {
    const normalized = normalizeOverrides(value);
    const candidate = normalizeOverrides({ [model]: efforts });
    const [normalizedModel] = Object.keys(candidate);
    normalized[normalizedModel] = candidate[normalizedModel];
    return normalizeOverrides(normalized);
  }

  function deleteOverride(value, model) {
    const normalized = normalizeOverrides(value);
    delete normalized[String(model).trim().toLowerCase()];
    return normalized;
  }

  const api = {
    REASONING_EFFORT_ORDER,
    normalizeOverrides,
    upsertOverride,
    deleteOverride
  };

  if (typeof module !== 'undefined' && module.exports) {
    module.exports = api;
  }
  if (root) root.ModelReasoningEfforts = api;
})(typeof window !== 'undefined' ? window : null);
