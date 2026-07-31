import {
  CheckCircleIcon,
  ExclamationTriangleIcon,
} from '@heroicons/react/24/outline';
import axios from 'axios';
import { useEffect, useState } from 'react';
import type { SettingsData } from './Settings';
import Button from './ui/Button';

interface TagSettingsProps {
  settings: SettingsData | null;
  loading: boolean;
  loadError: string | null;
  onSettingsSaved: (settings: SettingsData) => void;
}

interface SettingsResponse {
  settings: SettingsData;
}

export default function TagSettings({
  settings,
  loading,
  loadError,
  onSettingsSaved,
}: TagSettingsProps) {
  const [tagsAutoCreate, setTagsAutoCreate] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [success, setSuccess] = useState('');
  const [saveError, setSaveError] = useState('');

  useEffect(() => {
    if (settings && !dirty) {
      setTagsAutoCreate(settings.tags_auto_create);
    }
  }, [settings, dirty]);

  const handleToggle = (checked: boolean) => {
    setTagsAutoCreate(checked);
    setDirty(checked !== settings?.tags_auto_create);
    setSuccess('');
    setSaveError('');
  };

  const handleSave = async () => {
    if (!dirty) return;

    setSaving(true);
    setSuccess('');
    setSaveError('');
    try {
      const response = await axios.post<SettingsResponse>('./api/settings', {
        tags_auto_create: tagsAutoCreate,
      });
      setTagsAutoCreate(response.data.settings.tags_auto_create);
      setDirty(false);
      setSuccess('Tag settings saved.');
      onSettingsSaved(response.data.settings);
    } catch (error) {
      console.error('Error saving tag settings:', error);
      setSaveError('Could not save tag settings. Try again.');
    } finally {
      setSaving(false);
    }
  };

  return (
    <section
      aria-labelledby="tag-settings-heading"
      className="rounded-lg border border-line bg-surface p-6"
    >
      <div>
        <h2 id="tag-settings-heading" className="text-lg font-semibold">
          Tag suggestions
        </h2>
        <p className="mt-1 max-w-prose text-sm text-muted">
          Control whether reviewed AI suggestions may add tags that do not
          already exist in paperless-ngx.
        </p>
      </div>

      {loading && (
        <div className="mt-5 space-y-2" aria-busy="true">
          <div className="h-5 w-64 animate-pulse rounded bg-surface-2" />
          <div className="h-4 w-full max-w-xl animate-pulse rounded bg-surface-2" />
          <span className="sr-only">Loading tag settings…</span>
        </div>
      )}

      {loadError && !loading && (
        <p role="alert" className="mt-4 text-sm text-neg">
          {loadError}
        </p>
      )}

      {settings && !loading && (
        <>
          <label className="mt-5 flex cursor-pointer items-start gap-3">
            <input
              type="checkbox"
              checked={tagsAutoCreate}
              onChange={(event) => handleToggle(event.target.checked)}
              className="mt-0.5 h-5 w-5 shrink-0 cursor-pointer rounded accent-primary"
            />
            <span className="min-w-0">
              <span className="block text-sm font-medium">
                Automatically create new suggested tags
              </span>
              <span className="mt-1 block max-w-prose text-sm text-muted">
                When disabled, the AI and review workflow use only tags that
                already exist in paperless-ngx.
              </span>
            </span>
          </label>

          <div className="mt-4 flex items-start gap-2 border-l-2 border-warn bg-warn-tint px-3 py-2 text-xs text-warn">
            <ExclamationTriangleIcon
              className="mt-0.5 h-4 w-4 shrink-0"
              aria-hidden="true"
            />
            <p>
              Enabling this changes the paperless-ngx tag list when you apply
              a new AI-suggested tag.
            </p>
          </div>

          <div className="mt-5 flex flex-wrap items-center justify-between gap-3 border-t border-line pt-4">
            <div className="min-h-5 text-sm" aria-live="polite">
              {success && (
                <p role="status" className="inline-flex items-center gap-1.5 text-pos">
                  <CheckCircleIcon className="h-4 w-4" aria-hidden="true" />
                  {success}
                </p>
              )}
              {saveError && (
                <p role="alert" className="text-neg">
                  {saveError}
                </p>
              )}
            </div>
            <Button
              type="button"
              variant="primary"
              onClick={handleSave}
              disabled={!dirty}
              loading={saving}
            >
              {saving ? 'Saving' : 'Save tag settings'}
            </Button>
          </div>
        </>
      )}
    </section>
  );
}
