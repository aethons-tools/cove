import { Button, Group, Modal, Text } from "@mantine/core";

// Confirm is the admin's confirmation dialog.
export function Confirm({ opened, text, action, color, busy, onCancel, onConfirm }: {
  opened: boolean; text: string; action: string; color?: string; busy?: boolean; onCancel: () => void; onConfirm: () => void;
}) {
  return (
    <Modal opened={opened} onClose={onCancel} title="Are you sure?" centered>
      <Text size="sm">{text}</Text>
      <Group justify="flex-end" mt="md">
        <Button variant="default" onClick={onCancel}>Cancel</Button>
        <Button color={color} loading={busy} disabled={busy} onClick={onConfirm}>{action}</Button>
      </Group>
    </Modal>
  );
}
