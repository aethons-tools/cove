import { ActionIcon, Group, Modal, NativeSelect, Slider, Stack, Text } from "@mantine/core";
import { useState } from "react";
import { CHIMES, loadSounds, preview, storeSounds, type SoundKind, type SoundSettings } from "./sounds";

const OPTIONS = [{ value: "off", label: "Off" }, ...Object.keys(CHIMES).map((n) => ({ value: n, label: n[0]!.toUpperCase() + n.slice(1) }))];
const NAMES: Record<SoundKind, string> = { messages: "Messages", needs: "Needs you" };

// SoundsSettings is the account menu's per-device Sounds dialog: a chime (or
// Off) for each kind, a preview, and one volume. Every change is saved.
export function SoundsSettings({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const [s, setS] = useState<SoundSettings>(loadSounds);
  const save = (next: SoundSettings) => { setS(next); storeSounds(next); };
  return (
    <Modal opened={opened} onClose={onClose} title="Sounds">
      <Stack gap="sm">
        {(Object.keys(NAMES) as SoundKind[]).map((k) => (
          <Group key={k} gap="xs" align="flex-end">
            <NativeSelect flex={1} label={NAMES[k]} data={OPTIONS} value={s[k]}
              onChange={(e) => save({ ...s, [k]: e.currentTarget.value })} />
            <ActionIcon variant="default" size="lg" aria-label={`Play the ${NAMES[k]} sound`} onClick={() => preview(s[k], s.volume)}>▶</ActionIcon>
          </Group>
        ))}
        <div>
          <Text size="sm" fw={500} id="sounds-volume">Volume</Text>
          <Slider aria-labelledby="sounds-volume" min={0} max={100} value={Math.round(s.volume * 100)}
            onChange={(v) => setS({ ...s, volume: v / 100 })} onChangeEnd={(v) => save({ ...s, volume: v / 100 })} />
        </div>
        <Text size="xs" c="dimmed">Chimes play while Jam is open. With Jam closed, notifications use your system's sound.</Text>
      </Stack>
    </Modal>
  );
}
