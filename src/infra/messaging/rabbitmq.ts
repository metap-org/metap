import amqp from "amqplib";

export function createRabbitPublisher(url: string) {
  let connection: amqp.Connection | undefined;
  let channel: amqp.Channel | undefined;

  async function getChannel() {
    if (!connection) {
      connection = await amqp.connect(url);
    }

    if (!channel) {
      channel = await connection.createChannel();
      await channel.assertExchange("metap.events", "topic", { durable: true });
    }

    return channel;
  }

  return {
    async publish(topic: string, payload: unknown) {
      const currentChannel = await getChannel();
      currentChannel.publish("metap.events", topic, Buffer.from(JSON.stringify(payload)), {
        contentType: "application/json",
        persistent: true,
      });
    },
    async close() {
      await channel?.close();
      await connection?.close();
    },
  };
}

export type RabbitPublisher = ReturnType<typeof createRabbitPublisher>;
